package provider

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/provider/openai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol/auth/codex"
)

// CodexURL is the subscription-backed ChatGPT Responses endpoint.
const CodexURL = "https://chatgpt.com/backend-api/codex/responses"

// CodexOptions requires explicit credentials and transport dependencies. SessionID
// is the affinity key when a call does not provide its own promptCacheKey.
type CodexOptions struct {
	Credentials codex.Source
	HTTPClient  *http.Client
	URL         string
	UserAgent   string
	SessionID   string
}

// NewCodexModel creates an explicitly authenticated, stateless Responses model.
// It performs no credential lookup or network I/O until Stream is called.
func NewCodexModel(modelID string, opts CodexOptions) (stream.Model, error) {
	u, err := url.Parse(opts.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return nil, errors.New("codex: invalid Responses URL")
	}
	if modelID == "" || opts.Credentials == nil || opts.HTTPClient == nil || opts.UserAgent == "" || opts.SessionID == "" {
		return nil, errors.New("codex: model, Credentials, HTTPClient, UserAgent and SessionID are required")
	}
	client := *opts.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	model := openai.NewResponsesModel(modelID, openai.ResponsesConfig{
		Provider: "openai.responses", URL: opts.URL, HTTPClient: &client,
		ConfigureRequest: func(req *http.Request) error {
			access, err := opts.Credentials.Access(req.Context())
			if err != nil {
				return err
			}
			if access.Token == "" {
				return errors.New("codex: empty access token")
			}
			req.Header.Set("Authorization", "Bearer "+access.Token)
			req.Header.Del("ChatGPT-Account-Id")
			req.Header.Del("x-openai-internal-codex-residency")
			req.Header.Del("OpenAI-Organization")
			req.Header.Del("OpenAI-Project")
			if access.AccountID != "" {
				req.Header.Set("ChatGPT-Account-Id", access.AccountID)
			}
			if access.ComputeResidency != "" {
				req.Header.Set("x-openai-internal-codex-residency", access.ComputeResidency)
			}
			req.Header.Set("User-Agent", opts.UserAgent)
			req.Header.Set("originator", "sol")
			req.Header.Set("Accept", "text/event-stream")
			return nil
		},
	})
	return &codexModel{model: model, sessionID: opts.SessionID}, nil
}

type codexModel struct {
	model     stream.Model
	sessionID string
}

func (m *codexModel) ID() string       { return m.model.ID() }
func (m *codexModel) Provider() string { return m.model.Provider() }
func (m *codexModel) Stream(ctx context.Context, opts *stream.CallOptions) (<-chan stream.Event, error) {
	if opts == nil {
		return nil, errors.New("codex: call options are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.AbortSignal != nil {
		if err := opts.AbortSignal.Err(); err != nil {
			return nil, err
		}
	}
	copy := *opts
	copy.ProviderOptions = maps.Clone(opts.ProviderOptions)
	if copy.ProviderOptions == nil {
		copy.ProviderOptions = make(map[string]any)
	}
	for _, key := range []string{"conversation", "previousResponseId"} {
		if value, ok := copy.ProviderOptions[key]; ok && value != nil && value != "" {
			return nil, errors.New("codex: server-stored continuation is unsupported")
		}
	}
	var instructions []string
	if value, ok := copy.ProviderOptions["instructions"]; ok {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("codex: instructions must be text")
		}
		if text != "" {
			instructions = append(instructions, text)
		}
	}
	copy.Messages = make([]message.Message, 0, len(opts.Messages))
	for _, msg := range opts.Messages {
		if msg.Role != message.RoleSystem && msg.Role != "developer" {
			copy.Messages = append(copy.Messages, msg)
			continue
		}
		if len(msg.Content.Parts) == 0 {
			if msg.Content.Text != "" {
				instructions = append(instructions, msg.Content.Text)
			}
			continue
		}
		for _, part := range msg.Content.Parts {
			text, ok := part.(message.TextPart)
			if !ok {
				return nil, errors.New("codex: system messages must contain only text")
			}
			instructions = append(instructions, text.Text)
		}
	}
	if strings.TrimSpace(strings.Join(instructions, "\n\n")) == "" {
		return nil, errors.New("codex: nonempty instructions are required")
	}
	copy.ProviderOptions["instructions"] = strings.Join(instructions, "\n\n")
	copy.ProviderOptions["store"] = false
	if copy.ProviderOptions["reasoningEffort"] == "minimal" {
		copy.ProviderOptions["reasoningEffort"] = "low"
	}
	if copy.Reasoning == stream.ReasoningEffortMinimal {
		copy.Reasoning = stream.ReasoningEffortLow
	}
	// Codex does not accept output-token limits or sampling controls. Normalize
	// every caller, including title and compaction models, at the model boundary.
	copy.MaxOutputTokens = nil
	copy.Temperature = nil
	copy.TopP = nil
	for _, key := range []string{"maxToolCalls", "metadata", "logprobs", "truncation", "user", "safetyIdentifier", "promptCacheRetention"} {
		delete(copy.ProviderOptions, key)
	}
	copy.Headers = maps.Clone(opts.Headers)
	if copy.Headers == nil {
		copy.Headers = make(map[string]string)
	}
	sessionID := m.sessionID
	if value, ok := copy.ProviderOptions["promptCacheKey"]; ok {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("codex: promptCacheKey must be text")
		}
		if text != "" {
			sessionID = text
		}
	}
	copy.ProviderOptions["promptCacheKey"] = sessionID
	// Header keys are normalized before replacement to prevent differently cased
	// caller keys from shadowing the session affinity value.
	for key := range copy.Headers {
		if strings.EqualFold(key, "session-id") {
			delete(copy.Headers, key)
		}
	}
	copy.Headers["session-id"] = sessionID
	// One context governs credentials, refresh, HTTP and the complete stream.
	combined, cancel := context.WithCancelCause(ctx)
	stop := func() bool { return true }
	if opts.AbortSignal != nil {
		abort := opts.AbortSignal
		stop = context.AfterFunc(abort, func() { cancel(context.Cause(abort)) })
		if err := abort.Err(); err != nil {
			cancel(context.Cause(abort))
		}
	}
	cleanup := func() { stop(); cancel(nil) }
	copy.AbortSignal = combined
	if err := combined.Err(); err != nil {
		cleanup()
		return nil, context.Cause(combined)
	}
	events, err := m.model.Stream(combined, &copy)
	if err != nil {
		cleanup()
		return nil, err
	}
	output := make(chan stream.Event, 100)
	go func() {
		defer close(output)
		defer cleanup()
		// Drain after cancellation so the provider cannot remain blocked sending
		// buffered events when the downstream consumer stops reading.
		for event := range events {
			if combined.Err() != nil {
				continue
			}
			select {
			case output <- event:
			case <-combined.Done():
			}
		}
		if combined.Err() != nil {
			terminal := stream.Event{Type: stream.EventError, Data: stream.ErrorEvent{Error: context.Cause(combined)}}
			select {
			case output <- terminal:
			default:
				// Preserve the terminal error by freeing one buffered event. The
				// consumer may already have drained it; this is the only sender.
				select {
				case <-output:
				default:
				}
				output <- terminal
			}
		}
	}()
	return output, nil
}
