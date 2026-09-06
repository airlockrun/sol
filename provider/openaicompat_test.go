package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
)

type testRoundTripFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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
