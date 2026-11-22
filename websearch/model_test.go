package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol/internal/generation"
	"github.com/airlockrun/sol/provider"
)

type searchModel struct {
	call func(context.Context, *stream.CallOptions) (<-chan stream.Event, error)
}

type searchTransport func(*http.Request) (*http.Response, error)

func (f searchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNormalModelSearchTransport(t *testing.T) {
	for _, tc := range []struct{ backend, provider string }{{"openai", "openai"}, {"gemini", "google"}, {"grok", "xai"}, {"kimi", "moonshotai"}} {
		t.Run(tc.backend, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: searchTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if !strings.HasPrefix(r.URL.String(), "https://search.invalid/custom/") {
					t.Error("configured endpoint ignored", r.URL)
				}
				if tc.backend == "gemini" {
					if r.URL.Query().Get("key") != "key" {
						t.Error("missing google key")
					}
				} else if r.Header.Get("Authorization") != "Bearer key" {
					t.Error("missing bearer key")
				}
				return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad key","type":"authentication_error"}}`))}, nil
			})}
			model := provider.CreateModel(tc.provider, "test-model", provider.Options{APIKey: "key", BaseURL: "https://search.invalid/custom", HTTPClient: client})
			_, err := NewModelClient(tc.backend, model).Search(t.Context(), Request{Query: "q"})
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func (*searchModel) ID() string       { return "test" }
func (*searchModel) Provider() string { return "test" }
func (m *searchModel) Stream(ctx context.Context, opts *stream.CallOptions) (<-chan stream.Event, error) {
	return m.call(ctx, opts)
}

func TestModelSearch(t *testing.T) {
	for _, backend := range []string{"openai", "gemini", "grok"} {
		t.Run(backend, func(t *testing.T) {
			model := &searchModel{call: func(ctx context.Context, opts *stream.CallOptions) (<-chan stream.Event, error) {
				if len(opts.Tools) != 1 || !opts.Tools[0].IsProviderTool() {
					t.Fatal("native tool missing", opts.Tools)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing model wall deadline")
				}
				ch := make(chan stream.Event, 3)
				ch <- stream.Event{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "answer"}}
				ch <- stream.Event{Type: stream.EventSource, Data: stream.SourceEvent{URL: "https://example.com", Title: "source"}}
				ch <- stream.Event{Type: stream.EventFinish, Data: stream.FinishEvent{FinishReason: stream.FinishReasonStop}}
				close(ch)
				return ch, nil
			}}
			resp, err := NewModelClient(backend, model).Search(t.Context(), Request{Query: "question"})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Provider != backend || resp.Synthesis != "answer" || len(resp.Results) != 1 {
				t.Fatalf("%+v", resp)
			}
		})
	}
}

func TestModelSearchDeadlinesJoinProducer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		progress bool
		duration time.Duration
	}{
		{"idle", false, generation.DefaultIdle}, {"wall", true, generation.DefaultWall},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cancelled, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
				model := &searchModel{call: func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
					ch := make(chan stream.Event)
					go func() {
						defer close(ch)
						defer close(stopped)
						tick := time.NewTicker(time.Minute)
						defer tick.Stop()
						for {
							select {
							case <-ctx.Done():
								close(cancelled)
								<-release
								return
							case <-tick.C:
								if tc.progress {
									select {
									case ch <- stream.Event{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "x"}}:
									case <-ctx.Done():
									}
								}
							}
						}
					}()
					return ch, nil
				}}
				done := make(chan error, 1)
				start := time.Now()
				go func() {
					_, err := NewModelClient("gemini", model).Search(t.Context(), Request{Query: "q"})
					done <- err
				}()
				<-cancelled
				if elapsed := time.Since(start); elapsed != tc.duration {
					t.Fatalf("deadline %s, want %s", elapsed, tc.duration)
				}
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("returned before producer joined")
				default:
				}
				close(release)
				if err := <-done; !errors.Is(err, generation.ErrDeadline) {
					t.Fatal(err)
				}
				select {
				case <-stopped:
				default:
					t.Fatal("producer escaped")
				}
			})
		})
	}
}

func TestKimiNativeSearchProtocol(t *testing.T) {
	var requests int
	const args = `{"search_results":[{"title":"source","url":"https://example.com","content":"detail"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key" {
			t.Error("wrong endpoint/auth")
		}
		var body struct {
			Model    string            `json:"model"`
			Tools    []json.RawMessage `json:"tools"`
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
				Reasoning  string `json:"reasoning_content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "kimi-test" || len(body.Tools) != 1 || string(body.Tools[0]) != `{"type":"builtin_function","function":{"name":"$web_search"}}` {
			t.Errorf("native declaration: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"search plan\"}}]}\n\n")
			chunk := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "search-1", "type": "function", "function": map[string]any{"name": "$web_search", "arguments": args}}}}, "finish_reason": "tool_calls"}}}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
		} else {
			if len(body.Messages) > 1 && body.Messages[1].Reasoning != "search plan" {
				t.Error("reasoning was not preserved")
			}
			if len(body.Messages) != 3 || body.Messages[2].Role != "tool" || body.Messages[2].ToolCallID != "search-1" || body.Messages[2].Content != args {
				t.Errorf("builtin continuation: %+v", body.Messages)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	model := provider.CreateModel("moonshotai", "kimi-test", provider.Options{APIKey: "key", BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	resp, err := NewModelClient("kimi", model).Search(t.Context(), Request{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || resp.Synthesis != "answer" || len(resp.Results) != 1 || resp.Results[0].Snippet != "detail" {
		t.Fatalf("requests=%d response=%+v", requests, resp)
	}
}
