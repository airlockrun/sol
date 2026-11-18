package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/provider/openai"
	"github.com/airlockrun/goai/stream"
)

func TestValidateInputMIME(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []string
		mime  string
		want  string
	}{
		{"image declaration", []string{"image"}, "image/png", ""},
		{"PDF declaration", []string{"pdf"}, "application/pdf", ""},
		{"audio declaration", []string{"audio"}, "audio/wav", ""},
		{"video declaration", []string{"video"}, "video/mp4", ""},
		{"generic file declaration", []string{"file"}, "application/vnd.ms-excel", ""},
		{"text remains usable without metadata", nil, "text/plain; charset=utf-8", ""},
		{"unknown media capability", nil, "image/png", "no declared media"},
		{"unsupported PDF", []string{"text", "image"}, "application/pdf", "does not declare pdf"},
		{"not every application is PDF", []string{"pdf"}, "application/zip", "does not declare file"},
		{"file is not blanket audio", []string{"file"}, "audio/wav", "does not declare audio"},
		{"unknown MIME", []string{"file"}, "", "missing or invalid"},
		{"safe invalid MIME", []string{"file"}, "secret\nhttps://private?token=secret", "missing or invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var modalities *ModelModalities
			if tc.input != nil {
				modalities = &ModelModalities{Input: tc.input}
			}
			err := ValidateInputMIME(modalities, tc.mime)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe MIME diagnostic")
			}
		})
	}
}

type attachmentRoundTripper func(*http.Request) (*http.Response, error)

func (f attachmentRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInputModelRejectsBeforeBinaryHTTP(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    []string
		role     message.Role
		file     message.FilePart
		requests int
	}{
		{"declared image", []string{"image"}, message.RoleUser, message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "c2VjcmV0"}}, 1},
		{"missing capability", nil, message.RoleUser, message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "c2VjcmV0"}}, 0},
		{"unsupported selected model", []string{"text"}, message.RoleUser, message.FilePart{MimeType: "application/pdf", Data: message.FileDataBytes{Data: "c2VjcmV0"}}, 0},
		{"unsupported adapter MIME", []string{"file"}, message.RoleUser, message.FilePart{MimeType: "application/zip", Data: message.FileDataBytes{Data: "c2VjcmV0"}}, 0},
		{"unsupported role", []string{"image"}, message.RoleAssistant, message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "c2VjcmV0"}}, 0},
		{"provider reference not a URL", []string{"image"}, message.RoleUser, message.FilePart{MimeType: "image/png", Data: message.FileDataReference{Reference: map[string]any{"file_id": "secret"}}}, 0},
		{"host ref not a provider ID", []string{"image"}, message.RoleUser, message.FilePart{MimeType: "image/png", Data: message.FileDataURL{URL: "airlock-file:secret"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: attachmentRoundTripper(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"owned fixture"}}`))}, nil
			})}
			actual := openai.NewResponsesModel("fixture-model", openai.ResponsesConfig{Provider: "openai.responses", URL: "https://fixture.invalid/responses", HTTPClient: client, ConfigureRequest: func(*http.Request) error { return nil }})
			m := &inputModel{Model: actual, modalities: &ModelModalities{Input: tc.input}}
			events, err := m.Stream(t.Context(), &stream.CallOptions{Messages: []message.Message{{Role: tc.role, Content: message.Content{Parts: []message.Part{tc.file}}}}})
			if events != nil {
				for range events {
				}
			}
			if tc.requests == 0 && err == nil {
				t.Fatal("unsupported file must reject synchronously")
			}
			if requests != tc.requests {
				t.Fatalf("requests = %d, want %d; err=%v", requests, tc.requests, err)
			}
			if tc.requests == 0 && strings.Contains(err.Error(), "secret") {
				t.Fatal("payload in unsupported error")
			}
		})
	}
}

type streamOnlyAttachmentModel struct{ calls int }

func (*streamOnlyAttachmentModel) ID() string       { return "fixture" }
func (*streamOnlyAttachmentModel) Provider() string { return "fixture" }
func (m *streamOnlyAttachmentModel) Stream(context.Context, *stream.CallOptions) (<-chan stream.Event, error) {
	m.calls++
	ch := make(chan stream.Event)
	close(ch)
	return ch, nil
}

type fileOnlyAttachmentModel struct{ streamOnlyAttachmentModel }

func (*fileOnlyAttachmentModel) ValidateFile(message.Role, message.FilePart) error { return nil }

func TestInputModelFileOnlyContractCannotAdmitReplay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message message.Message
		allowed bool
	}{
		{"ordinary text remains independent", message.NewUserMessage("text"), true},
		{"file needs complete contract", message.NewUserMessageWithParts(message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "cHJlZmxpZ2h0"}}), false},
		{"tool replay needs complete contract", message.Message{Role: message.RoleTool, Content: message.Content{Parts: []message.Part{message.ToolResultPart{ToolCallID: "call", ToolName: "tool", Output: message.TextOutput{Value: "text"}}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := &fileOnlyAttachmentModel{}
			model := &inputModel{Model: actual, modalities: &ModelModalities{Input: []string{"image"}}}
			_, err := model.Stream(t.Context(), &stream.CallOptions{Messages: []message.Message{tc.message}})
			if (err == nil) != tc.allowed || (actual.calls != 0) != tc.allowed {
				t.Fatalf("allowed=%v calls=%d err=%v", tc.allowed, actual.calls, err)
			}
		})
	}
}

func TestOrdinaryTextRequestClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		msg      message.Message
		ordinary bool
	}{
		{"plain user", message.NewUserMessage("text"), true},
		{"plain text part", message.NewUserMessageWithParts(message.TextPart{Text: "text"}), true},
		{"text file is still a file", message.NewUserMessageWithParts(message.FilePart{MimeType: "text/plain", Data: message.FileDataText{Text: "text"}}), false},
		{"reasoning", message.Message{Role: message.RoleAssistant, Content: message.Content{Parts: []message.Part{message.ReasoningPart{Text: "reasoning"}}}}, false},
		{"tool call", message.Message{Role: message.RoleAssistant, Content: message.Content{Parts: []message.Part{message.ToolCallPart{ID: "call", Name: "tool"}}}}, false},
		{"approval", message.Message{Role: message.RoleAssistant, Content: message.Content{Parts: []message.Part{message.ToolApprovalRequestPart{}}}}, false},
		{"tool text", message.Message{Role: message.RoleTool, Content: message.Content{Text: "text"}}, false},
		{"unknown role", message.Message{Role: "unknown", Content: message.Content{Text: "text"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOrdinaryTextMessages([]message.Message{tc.msg}); got != tc.ordinary {
				t.Fatalf("ordinary=%v want=%v", got, tc.ordinary)
			}
		})
	}
}

func TestInputModelPreservesDeclaredNativeHostedReplay(t *testing.T) {
	result := message.ToolResultPart{ToolCallID: "hosted-call", ToolName: "web_search", ProviderExecuted: true, Output: message.TextOutput{Value: "result"}, ProviderOptions: map[string]any{"anthropic": map[string]any{"rawBlock": map[string]any{"type": "web_search_tool_result", "tool_use_id": "hosted-call", "content": []any{}}}}}
	messages := []message.Message{{Role: message.RoleAssistant, Content: message.Content{Parts: []message.Part{result}}}}
	for _, tc := range []struct {
		provider, model string
		allowed         bool
	}{
		{"anthropic", "claude-sonnet-4-6", true},
		{"google", "gemini-3-flash-preview", false},
		{"openai", "gpt-4.1", false},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			actual := CreateModel(tc.provider, tc.model, Options{})
			err := Validator(actual).ValidateMessages(messages)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}

func TestInputModelMissingAdapterContractIsUnknown(t *testing.T) {
	actual := &streamOnlyAttachmentModel{}
	m := &inputModel{Model: actual, modalities: &ModelModalities{Input: []string{"image"}}}
	_, err := m.Stream(t.Context(), &stream.CallOptions{Messages: []message.Message{message.NewUserMessageWithParts(message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "c2VjcmV0"}})}})
	if err == nil || actual.calls != 0 {
		t.Fatalf("unknown adapter admitted: %v, calls=%d", err, actual.calls)
	}
}

func TestSelectedModelChangeRejectsExistingHistoryWithoutMutation(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: attachmentRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"owned fixture"}}`))}, nil
	})}
	options := &stream.CallOptions{Messages: []message.Message{message.NewUserMessageWithParts(message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "c2VjcmV0"}})}}
	before, err := json.Marshal(options.Messages)
	if err != nil {
		t.Fatal(err)
	}
	actual := openai.NewResponsesModel("fixture-model", openai.ResponsesConfig{Provider: "openai.responses", URL: "https://fixture.invalid/responses", HTTPClient: client, ConfigureRequest: func(*http.Request) error { return nil }})
	vision := &inputModel{Model: actual, modalities: &ModelModalities{Input: []string{"text", "image"}}}
	events, err := vision.Stream(t.Context(), options)
	if events != nil {
		for range events {
		}
	}
	if requests != 1 {
		t.Fatalf("supported request count=%d err=%v", requests, err)
	}
	text := &inputModel{Model: actual, modalities: &ModelModalities{Input: []string{"text"}}}
	if _, err := text.Stream(t.Context(), options); err == nil || !strings.Contains(err.Error(), "does not declare image") {
		t.Fatalf("new selection error=%v", err)
	}
	if requests != 1 {
		t.Fatal("rejected history reached HTTP")
	}
	after, err := json.Marshal(options.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("model change rewrote durable history")
	}
}

func TestInputModalitiesCatalogSnapshot(t *testing.T) {
	got := InputModalities("openai", "gpt-4.1")
	if got == nil || !got.SupportsInput("image") || !got.SupportsInput("pdf") {
		t.Fatalf("modalities = %#v", got)
	}
	got.Input[0] = "modified"
	if GetModalities("openai", "gpt-4.1").Input[0] == "modified" {
		t.Fatal("catalog mutated")
	}
	if SupportsInputModality("no-such-provider", "no-such-model", "image") {
		t.Fatal("unknown model asserts image support")
	}
}

func TestAttachmentFlagDoesNotReplaceInputModalities(t *testing.T) {
	for _, tc := range []struct {
		name    string
		info    ModelInfo
		allowed bool
	}{
		{"explicit image without flag", ModelInfo{Attachment: false, Modalities: &ModelModalities{Input: []string{"text", "image"}}}, true},
		{"flag without declarations", ModelInfo{Attachment: true}, false},
		{"flag with text only", ModelInfo{Attachment: true, Modalities: &ModelModalities{Input: []string{"text"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInputMIME(tc.info.Modalities, "image/png")
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}
