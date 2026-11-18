package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/model"
	"github.com/airlockrun/goai/provider/openaicompat"
	"github.com/airlockrun/goai/stream"
)

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCompatibleProviderRequiresEndpoint(t *testing.T) {
	const missingProvider = "owned-test-missing-provider-cc3e737a"
	const missingEndpoint = "compatible provider requires an explicit BaseURL or a catalog API endpoint"
	type capture struct{ url, authorization string }
	requests := make(chan capture, 1)
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: testRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests <- capture{r.URL.String(), r.Header.Get("Authorization")}
		return nil, errors.New("owned request capture; no network")
	})}
	t.Cleanup(func() { http.DefaultClient = oldClient })
	for _, modality := range []struct {
		name, path string
		create     func(string, Options) func()
	}{
		{"language", "/chat/completions", func(id string, opts Options) func() {
			m := CreateModel(id, "owned-model", opts)
			return func() {
				events, err := m.Stream(t.Context(), &stream.CallOptions{Messages: []message.Message{message.NewUserMessage("owned text")}})
				if err == nil {
					for range events {
					}
				}
			}
		}},
		{"image", "/images/generations", func(id string, opts Options) func() {
			m := CreateImageModel(id, "owned-model", opts)
			return func() { _, _ = m.Generate(t.Context(), model.ImageCallOptions{Prompt: "owned text", N: 1}) }
		}},
		{"embedding", "/embeddings", func(id string, opts Options) func() {
			m := CreateEmbeddingModel(id, "owned-model", opts)
			return func() { _, _ = m.Embed(t.Context(), model.EmbedCallOptions{Values: []string{"owned text"}}) }
		}},
		{"speech", "/audio/speech", func(id string, opts Options) func() {
			m := CreateSpeechModel(id, "owned-model", opts)
			return func() { _, _ = m.Generate(t.Context(), model.SpeechCallOptions{Text: "owned text"}) }
		}},
		{"transcription", "/audio/transcriptions", func(id string, opts Options) func() {
			m := CreateTranscriptionModel(id, "owned-model", opts)
			return func() {
				_, _ = m.Transcribe(t.Context(), model.TranscribeCallOptions{Audio: []byte("owned audio"), MimeType: "audio/wav"})
			}
		}},
	} {
		t.Run(modality.name, func(t *testing.T) {
			for _, base := range []string{"", " \t\n", "/", " /// "} {
				t.Run("missing "+base, func(t *testing.T) {
					func() {
						defer func() {
							if got := recover(); got != missingEndpoint {
								t.Fatalf("panic = %v, want static endpoint error", got)
							}
						}()
						modality.create(missingProvider, Options{APIKey: "owned-private-key", BaseURL: base})
					}()
					select {
					case <-requests:
						t.Fatal("missing endpoint reached provider HTTP")
					default:
					}
				})
			}
			t.Run("explicit endpoint", func(t *testing.T) {
				modality.create(missingProvider, Options{APIKey: "owned-private-key", BaseURL: " https://owned.invalid/v1/ "})()
				select {
				case r := <-requests:
					if r.url != "https://owned.invalid/v1"+modality.path || r.authorization != "Bearer owned-private-key" {
						t.Fatal("explicit endpoint or credential not preserved")
					}
				default:
					t.Fatal("explicit endpoint did not reach owned transport")
				}
			})
			t.Run("native OpenAI default", func(t *testing.T) {
				modality.create("openai", Options{APIKey: "owned-openai-key"})()
				wantPath := modality.path
				if modality.name == "language" {
					wantPath = "/responses"
				}
				select {
				case r := <-requests:
					if r.url != "https://api.openai.com/v1"+wantPath || r.authorization != "Bearer owned-openai-key" {
						t.Fatal("explicitly selected OpenAI default changed")
					}
				default:
					t.Fatal("native default did not reach owned transport")
				}
			})
			t.Run("catalog endpoint with whitespace override", func(t *testing.T) {
				info, ok := GetProviderInfo("hetzner")
				if !ok || info.API == "" {
					t.Fatal("catalog fixture endpoint is unavailable")
				}
				modality.create("hetzner", Options{APIKey: "owned-catalog-key", BaseURL: " \t "})()
				select {
				case r := <-requests:
					if r.url != strings.TrimRight(strings.TrimSpace(info.API), "/")+modality.path || r.authorization != "Bearer owned-catalog-key" {
						t.Fatal("catalog endpoint or credential not preserved")
					}
				default:
					t.Fatal("catalog endpoint did not reach owned transport")
				}
			})
		})
	}

	t.Run("resolved Tempr catalog endpoint and removal", func(t *testing.T) {
		for _, tc := range []struct {
			name, base string
			missing    bool
		}{
			{"resolved catalog", "https://api.temprhq.io/v1", false},
			{"missing catalog", "", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				func() {
					defer func() {
						if got := recover(); tc.missing {
							if got != missingEndpoint {
								t.Fatalf("panic = %v", got)
							}
						} else if got != nil {
							t.Fatalf("unexpected panic: %v", got)
						}
					}()
					p := newOpenAICompatProvider("tempr", tc.base, "owned-tempr-key", nil, openaicompat.Options{})
					_, _ = p.EmbeddingModel("google/gemini-embedding-001").Embed(t.Context(), model.EmbedCallOptions{Values: []string{"owned text"}})
				}()
				select {
				case r := <-requests:
					if tc.missing || r.url != "https://api.temprhq.io/v1/embeddings" || r.authorization != "Bearer owned-tempr-key" {
						t.Fatal("catalog removal leaked or rerouted credential")
					}
				default:
					if !tc.missing {
						t.Fatal("resolved catalog endpoint did not reach owned transport")
					}
				}
			})
		}
	})
	t.Run("supplied modality provider does not permit missing endpoint", func(t *testing.T) {
		defer func() {
			if got := recover(); got != missingEndpoint {
				t.Fatalf("panic = %v", got)
			}
		}()
		newOpenAICompatProvider("owned", "", "owned-private-key", createProvider("openai", Options{}), openaicompat.Options{})
	})
	t.Run("malformed explicit endpoint is not substituted", func(t *testing.T) {
		p := createProvider(missingProvider, Options{BaseURL: "://owned-invalid-url", APIKey: "owned-private-key"}).(*openAICompatProvider)
		if p.compat.BaseURL() != "://owned-invalid-url" {
			t.Fatal("malformed explicit endpoint was silently substituted")
		}
		_, _ = p.EmbeddingModel("owned-model").Embed(t.Context(), model.EmbedCallOptions{Values: []string{"owned text"}})
		select {
		case <-requests:
			t.Fatal("malformed endpoint reached provider HTTP")
		default:
		}
	})
}

// TestCreateProvider_OpenRouterRoutesToCompat pins the OpenRouter fix: it must
// route through the OpenAI-compatible wrapper at OpenRouter's base URL (and the
// supplied key), never the openai package / api.openai.com.
func TestCreateProvider_OpenRouterRoutesToCompat(t *testing.T) {
	p := createProvider("openrouter", Options{APIKey: "sk-or-test"})
	cp, ok := p.(*openAICompatProvider)
	if !ok {
		t.Fatalf("openrouter provider type = %T, want *openAICompatProvider", p)
	}
	if cp.ID() != "openrouter" {
		t.Errorf("ID = %q, want openrouter", cp.ID())
	}
	if got := cp.compat.BaseURL(); got != OpenRouterBaseURL {
		t.Errorf("base URL = %q, want %q (default)", got, OpenRouterBaseURL)
	}
	if got := cp.compat.APIKey(); got != "sk-or-test" {
		t.Errorf("api key = %q, not propagated", got)
	}
}

// An explicit base URL (e.g. an OpenRouter-compatible proxy) overrides the
// default.
func TestCreateProvider_OpenRouterBaseURLOverride(t *testing.T) {
	p := createProvider("openrouter", Options{APIKey: "k", BaseURL: "https://proxy.test/v1"})
	cp := p.(*openAICompatProvider)
	if got := cp.compat.BaseURL(); got != "https://proxy.test/v1" {
		t.Errorf("base URL = %q, want the override", got)
	}
}

func TestOpenRouterDeclaresOnlyKnownEndpointUserImageTransport(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		role       message.Role
		file       message.FilePart
		allowed    bool
	}{
		{"default PNG bytes", "", message.RoleUser, message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "cHJlZmxpZ2h0"}}, true},
		{"known endpoint JPEG URL", OpenRouterBaseURL + "/", message.RoleUser, message.FilePart{MimeType: "image/jpeg", Data: message.FileDataURL{URL: "https://attachment.invalid/file"}}, true},
		{"unknown endpoint", "https://unknown.invalid/v1", message.RoleUser, message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "cHJlZmxpZ2h0"}}, false},
		{"tool media not declared", "", message.RoleTool, message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "cHJlZmxpZ2h0"}}, false},
		{"assistant media not declared", "", message.RoleAssistant, message.FilePart{MimeType: "image/png", Data: message.FileDataBytes{Data: "cHJlZmxpZ2h0"}}, false},
		{"PDF not inferred", "", message.RoleUser, message.FilePart{MimeType: "application/pdf", Data: message.FileDataBytes{Data: "cHJlZmxpZ2h0"}}, false},
		{"video not inferred", "", message.RoleUser, message.FilePart{MimeType: "video/mp4", Data: message.FileDataBytes{Data: "cHJlZmxpZ2h0"}}, false},
		{"provider reference not inferred", "", message.RoleUser, message.FilePart{MimeType: "image/png", Data: message.FileDataReference{Reference: map[string]any{"file_id": "private"}}}, false},
		{"UTF-8 text source", "", message.RoleUser, message.FilePart{MimeType: "text/plain", Data: message.FileDataText{Text: "text"}}, true},
		{"UTF-8 text bytes on override", "https://unknown.invalid/v1", message.RoleUser, message.FilePart{MimeType: "text/plain", Data: message.FileDataBytes{Data: "dGV4dA=="}}, true},
		{"binary is not UTF-8 text", "", message.RoleUser, message.FilePart{MimeType: "text/plain", Data: message.FileDataBytes{Data: "/w=="}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := CreateModel("openrouter", "fixture-model", Options{BaseURL: tc.base, InputModalities: &ModelModalities{Input: []string{"text", "image", "pdf", "video"}}})
			err := Validator(actual).ValidateFile(tc.role, tc.file)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}

func TestUnknownCompatibleTransportIsNotDeclaredByModelOrURL(t *testing.T) {
	for _, id := range []string{"openai-compatible", "unknown-compatible-catalog-id"} {
		t.Run(id, func(t *testing.T) {
			actual := CreateModel(id, "fixture-model", Options{BaseURL: OpenRouterBaseURL, InputModalities: &ModelModalities{Input: []string{"text", "image", "video"}}})
			for _, mime := range []string{"image/png", "video/mp4"} {
				if err := Validator(actual).ValidateFile(message.RoleUser, message.FilePart{MimeType: mime, Data: message.FileDataBytes{Data: "cHJlZmxpZ2h0"}}); err == nil {
					t.Fatalf("model declaration or URL asserted unknown %s transport", mime)
				}
			}
		})
	}
}

// An unregistered provider with a configured base URL is treated as
// OpenAI-compatible (chat/completions), not routed to api.openai.com.
func TestCreateProvider_UnknownIsOpenAICompat(t *testing.T) {
	p := createProvider("some-gateway", Options{APIKey: "k", BaseURL: "https://gw.test/v1"})
	cp, ok := p.(*openAICompatProvider)
	if !ok {
		t.Fatalf("unknown provider type = %T, want *openAICompatProvider", p)
	}
	if got := cp.compat.BaseURL(); got != "https://gw.test/v1" {
		t.Errorf("base URL = %q, want the configured value", got)
	}
}

// The real openai provider is unchanged — it must keep using the openai package
// (Responses API), not the compat wrapper.
func TestCreateProvider_OpenAIUnchanged(t *testing.T) {
	p := createProvider("openai", Options{APIKey: "k"})
	if _, ok := p.(*openAICompatProvider); ok {
		t.Fatal("openai must not be routed through the OpenAI-compatible wrapper")
	}
}

func TestCreateProvider_OpenAICompatibleRequiresBaseURL(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("createProvider() did not panic without BaseURL")
		}
	}()
	createProvider("openai-compatible", Options{})
}

func TestCreateProvider_OpenAICompatibleRoutesConservatively(t *testing.T) {
	type capturedRequest struct {
		path          string
		authorization []string
		body          map[string]any
	}
	requests := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := capturedRequest{
			path:          r.URL.Path,
			authorization: r.Header.Values("Authorization"),
			body:          map[string]any{},
		}
		if err := json.NewDecoder(r.Body).Decode(&request.body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- request
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	model := CreateModel("openai-compatible", "local-model", Options{BaseURL: server.URL + "/"})
	events, err := model.Stream(context.Background(), &stream.CallOptions{
		Messages: []message.Message{message.NewUserMessage("Hello")},
		ResponseFormat: &stream.ResponseFormat{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == stream.EventError {
			t.Fatal(event.Data.(stream.ErrorEvent).Error)
		}
	}

	request := <-requests
	if request.path != "/chat/completions" {
		t.Errorf("request path = %q, want /chat/completions", request.path)
	}
	if len(request.authorization) != 0 {
		t.Errorf("Authorization headers = %q, want none", request.authorization)
	}
	if _, ok := request.body["stream_options"]; ok {
		t.Errorf("stream_options = %v, want omitted", request.body["stream_options"])
	}
	responseFormat, ok := request.body["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_object" {
		t.Errorf("response_format = %v, want conservative json_object", request.body["response_format"])
	}
}

func TestCreateProvider_OpenAICompatibleOptions(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestBody <- body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	includeUsage := true
	supportsStructuredOutputs := true
	model := CreateModel("openai-compatible", "local-model", Options{
		BaseURL:                   server.URL,
		SupportsStructuredOutputs: &supportsStructuredOutputs,
		IncludeUsage:              &includeUsage,
	})
	events, err := model.Stream(context.Background(), &stream.CallOptions{
		Messages: []message.Message{message.NewUserMessage("Hello")},
		ResponseFormat: &stream.ResponseFormat{
			Type:   "json",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == stream.EventError {
			t.Fatal(event.Data.(stream.ErrorEvent).Error)
		}
	}

	body := <-requestBody
	options, ok := body["stream_options"].(map[string]any)
	if !ok || options["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage true", body["stream_options"])
	}
	responseFormat, ok := body["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_schema" {
		t.Errorf("response_format = %v, want json_schema", body["response_format"])
	}
}

func TestCreateProvider_OpenAICompatibleUsesHTTPClient(t *testing.T) {
	called := false
	client := &http.Client{Transport: testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n")),
			Request:    req,
		}, nil
	})}
	model := CreateModel("openai-compatible", "local-model", Options{
		BaseURL:    "http://local-model.test/v1",
		HTTPClient: client,
	})
	events, err := model.Stream(context.Background(), &stream.CallOptions{
		Messages: []message.Message{message.NewUserMessage("Hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == stream.EventError {
			t.Fatal(event.Data.(stream.ErrorEvent).Error)
		}
	}
	if !called {
		t.Fatal("configured HTTP client was not called")
	}
}
