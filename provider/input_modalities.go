package provider

import (
	"context"
	"errors"
	"mime"
	"strings"

	"github.com/airlockrun/goai/message"
	goaiprovider "github.com/airlockrun/goai/provider"
	"github.com/airlockrun/goai/stream"
)

// ModelValidator includes both file transport and complete-message placement.
type ModelValidator interface {
	goaiprovider.FileValidator
	goaiprovider.MessageValidator
}

var errMissingMessageValidator = errors.New("selected adapter has no declared complete-message contract for files or structured replay; select a supported model or explicitly read/extract text")

// Validator preserves the actual instantiated model's contracts. Missing
// interfaces remain unknown and cannot admit files or structured replay.
func Validator(model stream.Model) ModelValidator {
	if validator, ok := model.(ModelValidator); ok {
		return validator
	}
	return modelValidator{model: model}
}

type modelValidator struct{ model stream.Model }

func (v modelValidator) ValidateFile(role message.Role, file message.FilePart) error {
	if declared, ok := v.model.(goaiprovider.FileValidator); ok {
		return declared.ValidateFile(role, file)
	}
	return errors.New("selected adapter has no declared file transport; select a supported model or explicitly read/extract text")
}

func (v modelValidator) ValidateMessages(messages []message.Message) error {
	if declared, ok := v.model.(goaiprovider.MessageValidator); ok {
		return declared.ValidateMessages(messages)
	}
	return errMissingMessageValidator
}

type inputModel struct {
	stream.Model
	modalities *ModelModalities
}

func (m *inputModel) Stream(ctx context.Context, options *stream.CallOptions) (<-chan stream.Event, error) {
	if options == nil {
		return nil, errors.New("model call options are required")
	}
	if err := ValidateRequestMessages(m, options.Messages); err != nil {
		return nil, err
	}
	return m.Model.Stream(ctx, options)
}

func (m *inputModel) ValidateFile(role message.Role, file message.FilePart) error {
	if err := ValidateInputMIME(m.modalities, file.MimeType); err != nil {
		return err
	}
	if err := Validator(m.Model).ValidateFile(role, file); err != nil {
		return errors.New("selected adapter does not support this file MIME, source or message role; select a compatible model or explicitly read/extract text")
	}
	return nil
}

func (m *inputModel) ValidateMessages(messages []message.Message) error {
	if err := goaiprovider.ValidateMessageFiles(messages, m); err != nil {
		return err
	}
	if err := Validator(m.Model).ValidateMessages(messages); err != nil {
		if errors.Is(err, errMissingMessageValidator) {
			return err
		}
		return errors.New("selected adapter cannot represent the complete message content or placement; select a compatible model or explicitly read/extract text")
	}
	return nil
}

// ValidateRequestMessages permits the missing-contract exception only for an
// unchanged ordinary text request. Materializers must classify the original
// request before projection and keep file-bearing/structured requests strict.
func ValidateRequestMessages(validator ModelValidator, messages []message.Message) error {
	err := validator.ValidateMessages(messages)
	if errors.Is(err, errMissingMessageValidator) && IsOrdinaryTextMessages(messages) {
		return nil
	}
	return err
}

// IsOrdinaryTextMessages excludes files and every structured replay variant.
func IsOrdinaryTextMessages(messages []message.Message) bool {
	for _, msg := range messages {
		switch msg.Role {
		case message.RoleSystem, message.RoleUser, message.RoleAssistant, "developer":
		default:
			return false
		}
		for _, part := range msg.Content.Parts {
			if _, ok := part.(message.TextPart); !ok {
				return false
			}
		}
	}
	return true
}

// InputModalities returns an independent snapshot of the selected catalog model's
// input declaration. Missing declarations remain unknown, not media-capable.
func InputModalities(providerID, modelID string) *ModelModalities {
	declared := GetModalities(providerID, modelID)
	if declared == nil {
		return nil
	}
	return &ModelModalities{Input: append([]string(nil), declared.Input...)}
}

// ValidateInputMIME checks model intent only. The actual adapter must also accept
// the MIME, source and message role. Attachment is coarse catalog metadata;
// explicit input modalities establish model support, including image-only models
// whose catalog entry does not set Attachment.
func ValidateInputMIME(declared *ModelModalities, mediaType string) error {
	mediaType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return errors.New("attachment MIME type is missing or invalid; supply its actual content type")
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "text/") {
		return nil
	}
	modality := "file"
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		modality = "image"
	case strings.HasPrefix(mediaType, "audio/"):
		modality = "audio"
	case strings.HasPrefix(mediaType, "video/"):
		modality = "video"
	case mediaType == "application/pdf":
		modality = "pdf"
	}
	if declared == nil || len(declared.Input) == 0 {
		return errors.New("selected model has no declared media input capabilities; select a model with explicit input modalities or read the file as text")
	}
	if !declared.SupportsInput(modality) {
		// Only static modality labels enter this diagnostic, never file metadata.
		return errors.New("selected model does not declare " + modality + " input; select a compatible model or explicitly read/extract text")
	}
	return nil
}
