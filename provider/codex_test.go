package provider_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/agent"
	"github.com/airlockrun/sol/auth/codex"
	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/session"
	"github.com/airlockrun/sol/tools"
)

type fixedCodexSource struct {
	access codex.Access
	err    error
}

func (s fixedCodexSource) Access(ctx context.Context) (codex.Access, error) {
	if err := ctx.Err(); err != nil {
		return codex.Access{}, err
	}
	return s.access, s.err
}
func newCodexTestModel(t *testing.T, server *httptest.Server, source codex.Source) stream.Model {
	t.Helper()
	m, err := provider.NewCodexModel("gpt-5.4", provider.CodexOptions{Credentials: source, HTTPClient: server.Client(), URL: server.URL + "/backend-api/codex/responses", UserAgent: "sol/test", SessionID: "test-session"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func writeCodexText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg","phase":"final_answer"}}`)
	data, _ := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": text})
	fmt.Fprintf(w, "data: %s\n\n", data)
	fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg"}}`)
	fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.completed","response":{"id":"resp","usage":{"input_tokens":10,"output_tokens":2}}}`)
}

func TestCodexWirePolicy(t *testing.T) {
	for _, routed := range []bool{true, false} {
		t.Run(fmt.Sprint(routed), func(t *testing.T) {
			access := codex.Access{Token: "test-access"}
			if routed {
				access.AccountID = "account"
				access.ComputeResidency = "eu"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/backend-api/codex/responses" || r.Header.Get("Authorization") != "Bearer test-access" || r.Header.Get("ChatGPT-Account-Id") != access.AccountID || r.Header.Get("x-openai-internal-codex-residency") != access.ComputeResidency || r.Header.Get("OpenAI-Project") != "" || r.Header.Get("OpenAI-Organization") != "" || r.Header.Get("User-Agent") != "sol/test" || r.Header.Get("originator") != "sol" || r.Header.Get("session-id") != "caller-session" {
					t.Errorf("unexpected request headers: %v", r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["instructions"] != "explicit\n\nsystem\n\nparts\n\ndeveloper" || body["store"] != false || body["stream"] != true || body["prompt_cache_key"] != "caller-session" {
					t.Errorf("unexpected policy: %v", body)
				}
				for _, key := range []string{"max_output_tokens", "temperature", "top_p", "metadata", "max_tool_calls", "truncation", "user", "safety_identifier", "prompt_cache_retention", "top_logprobs"} {
					if _, ok := body[key]; ok {
						t.Errorf("unsupported field %s", key)
					}
				}
				if body["reasoning"].(map[string]any)["effort"] != "low" {
					t.Error("minimal effort not normalized")
				}
				input := body["input"].([]any)
				if len(input) != 1 || input[0].(map[string]any)["role"] != "user" {
					t.Errorf("system leaked into input: %v", input)
				}
				writeCodexText(w, "ok")
			}))
			defer server.Close()
			m := newCodexTestModel(t, server, fixedCodexSource{access: access})
			limit := 50
			temperature := 0.8
			opts := &stream.CallOptions{Messages: []message.Message{message.NewSystemMessage("system"), {Role: message.RoleSystem, Content: message.Content{Parts: []message.Part{message.TextPart{Text: "parts"}}}}, {Role: "developer", Content: message.Content{Text: "developer"}}, message.NewUserMessage("hi")}, MaxOutputTokens: &limit, Temperature: &temperature, TopP: &temperature,
				Headers:         map[string]string{"authorization": "wrong", "ChatGPT-Account-Id": "wrong", "x-openai-internal-codex-residency": "wrong", "OpenAI-Project": "wrong", "OpenAI-Organization": "wrong", "Session-ID": "wrong"},
				ProviderOptions: map[string]any{"instructions": "explicit", "promptCacheKey": "caller-session", "store": true, "reasoningEffort": "minimal", "metadata": map[string]string{"test": "value"}, "maxToolCalls": 2, "truncation": "auto", "user": "u", "safetyIdentifier": "s", "promptCacheRetention": "24h", "logprobs": 5}}
			before, _ := json.Marshal(opts)
			events, err := m.Stream(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			var text string
			for event := range events {
				switch e := event.Data.(type) {
				case stream.TextDeltaEvent:
					text += e.Text
				case stream.ErrorEvent:
					t.Fatal(e.Error)
				}
			}
			after, _ := json.Marshal(opts)
			if string(before) != string(after) {
				t.Fatal("caller options mutated")
			}
			if text != "ok" {
				t.Fatal(text)
			}
		})
	}
}

func TestCodexRejectsInvalidOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid call reached network") }))
	defer server.Close()
	m := newCodexTestModel(t, server, fixedCodexSource{access: codex.Access{Token: "test"}})
	for _, tc := range []struct {
		name string
		opts *stream.CallOptions
	}{
		{"nil", nil}, {"missing instructions", &stream.CallOptions{Messages: []message.Message{message.NewUserMessage("hi")}}},
		{"file system", &stream.CallOptions{Messages: []message.Message{{Role: message.RoleSystem, Content: message.Content{Parts: []message.Part{message.FilePart{}}}}}}},
		{"conversation", &stream.CallOptions{ProviderOptions: map[string]any{"instructions": "rules", "conversation": "c"}}},
		{"previous response", &stream.CallOptions{ProviderOptions: map[string]any{"instructions": "rules", "previousResponseId": "r"}}},
		{"bad instructions", &stream.CallOptions{ProviderOptions: map[string]any{"instructions": 12}}},
		{"bad cache key", &stream.CallOptions{ProviderOptions: map[string]any{"instructions": "rules", "promptCacheKey": 12}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Stream(t.Context(), tc.opts); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := provider.NewCodexModel("gpt-5.4", provider.CodexOptions{}); err == nil {
		t.Fatal("accepted missing dependencies")
	}
}

func TestCodexAuthErrorsAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source fixedCodexSource
	}{{"missing token", fixedCodexSource{}}, {"logged out", fixedCodexSource{err: codex.ErrNotLoggedIn}}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unauthenticated network request") }))
			defer server.Close()
			events, err := newCodexTestModel(t, server, tc.source).Stream(t.Context(), &stream.CallOptions{Messages: []message.Message{message.NewSystemMessage("rules")}})
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for event := range events {
				if _, ok := event.Data.(stream.ErrorEvent); ok {
					failed = true
				}
			}
			if !failed {
				t.Fatal("missing auth error")
			}
		})
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed credential redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	events, err := newCodexTestModel(t, server, fixedCodexSource{access: codex.Access{Token: "test"}}).Stream(t.Context(), &stream.CallOptions{Messages: []message.Message{message.NewSystemMessage("rules")}})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
}

func TestCodexToolAndReasoningRoundTrip(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, frame := range []string{
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs"}}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs","encrypted_content":"encrypted-state"}}`,
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"comment","phase":"commentary"}}`,
				`{"type":"response.output_text.delta","delta":"Checking."}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"comment"}}`,
				`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc","call_id":"call","name":"lookup","namespace":"local"}}`,
				`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{}"}`,
				`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc","call_id":"call","name":"lookup","namespace":"local","arguments":"{}"}}`,
				`{"type":"response.completed","response":{"id":"first","usage":{"input_tokens":10,"output_tokens":5}}}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", frame)
			}
			return
		}
		found := map[string]bool{}
		for _, raw := range body["input"].([]any) {
			item := raw.(map[string]any)
			if item["id"] != nil {
				t.Error("nonpersisted item ID replayed")
			}
			switch item["type"] {
			case "reasoning":
				found["reasoning"] = item["encrypted_content"] == "encrypted-state"
			case "function_call":
				found["call"] = item["call_id"] == "call" && item["namespace"] == "local"
			case "function_call_output":
				found["output"] = item["call_id"] == "call" && item["output"] == "result"
			}
			if item["role"] == "assistant" {
				found["phase"] = item["phase"] == "commentary"
			}
		}
		if !reflect.DeepEqual(found, map[string]bool{"reasoning": true, "call": true, "output": true, "phase": true}) {
			t.Errorf("lost replay data: %v, body=%v", found, body)
		}
		writeCodexText(w, "Finished.")
	}))
	defer server.Close()
	m := newCodexTestModel(t, server, fixedCodexSource{access: codex.Access{Token: "test"}})
	runner := sol.NewRunner(sol.RunnerOptions{Agent: &agent.Agent{Name: "test", Model: "openai/gpt-5.4", SystemPrompt: "rules", EnvironmentPrompt: "test", MaxSteps: 3, Tools: tool.Set{"lookup": {Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage, tool.CallOptions) (tool.Result, error) {
		return tool.Result{Output: "result"}, nil
	}}}}, Model: m, Quiet: true})
	result, err := runner.Run(t.Context(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != sol.RunCompleted || calls.Load() != 2 {
		t.Fatalf("status=%s calls=%d", result.Status, calls.Load())
	}
}

type codexReadImageStore struct {
	data []byte
}

func (s *codexReadImageStore) Load(ctx context.Context) ([]session.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var messages []session.Message
	if len(s.data) != 0 {
		if err := json.Unmarshal(s.data, &messages); err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *codexReadImageStore) Append(ctx context.Context, messages []session.Message) error {
	history, err := s.Load(ctx)
	if err != nil {
		return err
	}
	s.data, err = json.Marshal(append(history, messages...))
	return err
}

func (s *codexReadImageStore) Compact(ctx context.Context, messages []session.Message, _ int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	s.data, err = json.Marshal(messages)
	return err
}

func TestCodexRunnerReadNativeImagePersistenceAndCompaction(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{B: 255, A: 255})
	var fixture bytes.Buffer
	if err := png.Encode(&fixture, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "native.png")
	if err := os.WriteFile(path, fixture.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(fixture.Bytes())
	arguments, _ := json.Marshal(tools.ReadInput{FilePath: path})
	requests := make(chan map[string]any, 5)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-image-test" || body["instructions"] == nil || body["store"] != false || body["max_output_tokens"] != nil {
			t.Error("Codex policy did not preserve native image request normalization")
		}
		requests <- body
		call := calls.Add(1)
		if call == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			item := map[string]any{"type": "function_call", "id": "read-item", "call_id": "read-image", "name": "read"}
			for _, frame := range []map[string]any{
				{"type": "response.output_item.added", "output_index": 0, "item": item},
				{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": string(arguments)},
				{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "read-item", "call_id": "read-image", "name": "read", "arguments": string(arguments)}},
				{"type": "response.completed", "response": map[string]any{"id": "read-response", "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}},
			} {
				data, _ := json.Marshal(frame)
				fmt.Fprintf(w, "data: %s\n\n", data)
			}
			return
		}
		if call == 4 {
			writeCodexText(w, "Image inspected in the summary.")
		} else {
			writeCodexText(w, "Local protocol fixture complete.")
		}
	}))
	defer server.Close()
	model := newCodexTestModel(t, server, fixedCodexSource{access: codex.Access{Token: "synthetic-image-test"}})
	store := &codexReadImageStore{}
	newRunner := func(retainTurns int) *sol.Runner {
		ts := tool.Set{"read": tools.Read()}
		return sol.NewRunner(sol.RunnerOptions{
			Agent: &agent.Agent{Name: "image-test", Model: "openai/gpt-5.4", SystemPrompt: "Inspect images with the read tool.", MaxSteps: 3, Tools: ts, HistoryPolicy: agent.HistoryPolicy{FilesRetainTurns: retainTurns}},
			Model: model, Executor: tool.NewLocalExecutor(ts, nil), SessionStore: store, Quiet: true,
		})
	}
	runner := newRunner(0)
	result, err := runner.Run(t.Context(), "Read the generated image.")
	if err != nil || result.Status != sol.RunCompleted || calls.Load() != 2 {
		t.Fatalf("result = %+v, error = %v, HTTP calls = %d", result, err, calls.Load())
	}
	<-requests // The first request asks the model to select a tool.
	assertCodexReadImageInput(t, <-requests, encoded)
	if !bytes.Contains(store.data, []byte(encoded)) || !bytes.Contains(store.data, []byte(`"outputType":"content"`)) {
		t.Fatal("persisted read result lost structured image bytes")
	}
	persisted := bytes.Clone(store.data)
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range session.MessagesToGoAI(loaded) {
		for _, part := range msg.Content.Parts {
			if output, ok := part.(message.ToolResultPart); ok && output.ToolCallID == "read-image" {
				content, ok := output.Output.(message.ContentOutput)
				if !ok || len(content.Value) != 2 || content.Value[1].Type != "image-data" || content.Value[1].Data != encoded || content.Value[1].MediaType != "image/png" {
					t.Fatal("persisted tool image was flattened on session replay")
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("persisted read result missing")
	}
	// A fresh runner must send the saved image without rereading the file.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replay := newRunner(0)
	if _, err := replay.Run(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	assertCodexReadImageInput(t, <-requests, encoded)
	if _, err := replay.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertCodexReadImageInput(t, <-requests, encoded)
	if bytes.Contains(store.data, []byte(encoded)) || !bytes.Contains(store.data, []byte("Image inspected in the summary.")) {
		t.Fatal("compaction did not replace image context with its summary")
	}
	// Retention strips only model context; durable image data stays available.
	store.data = persisted
	if err := store.Append(t.Context(), []session.Message{{Role: "user", Content: "A later turn."}}); err != nil {
		t.Fatal(err)
	}
	if _, err := newRunner(1).Run(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	retained, _ := json.Marshal(<-requests)
	if bytes.Contains(retained, []byte(encoded)) || !bytes.Contains(retained, []byte("Image removed from context")) || !bytes.Contains(store.data, []byte(encoded)) {
		t.Fatal("image retention flattened bytes or mutated durable history")
	}
	if calls.Load() != 5 {
		t.Fatalf("HTTP calls = %d, want 5", calls.Load())
	}
}

func assertCodexReadImageInput(t *testing.T, body map[string]any, encoded string) {
	t.Helper()
	input, ok := body["input"].([]any)
	if !ok {
		t.Fatal("Responses input missing")
	}
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "function_call_output" || item["call_id"] != "read-image" {
			continue
		}
		parts, ok := item["output"].([]any)
		if !ok || len(parts) != 2 {
			t.Fatal("image tool result was flattened instead of native multipart output")
		}
		text, textOK := parts[0].(map[string]any)
		img, imageOK := parts[1].(map[string]any)
		if !textOK || !imageOK || text["type"] != "input_text" || !strings.Contains(fmt.Sprint(text["text"]), "Image attached:") || img["type"] != "input_image" || img["image_url"] != "data:image/png;base64,"+encoded {
			t.Fatal("native image bytes, MIME type or tool result ordering changed")
		}
		return
	}
	t.Fatal("read-image function result missing from Codex request")
}

func TestCodexCompaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["max_output_tokens"] != nil || !strings.Contains(body["instructions"].(string), "summary") {
			t.Errorf("bad compaction policy: %v", body)
		}
		writeCodexText(w, "summary")
	}))
	defer server.Close()
	m := newCodexTestModel(t, server, fixedCodexSource{access: codex.Access{Token: "test"}})
	s := session.New("test", "test", "gpt-5.4", session.ModelLimits{})
	defer s.Cancel()
	text, err := s.Compact(t.Context(), m, []goai.Message{goai.NewSystemMessage("original"), goai.NewUserMessage("history")}, &session.CompactOptions{MaxOutputTokens: 100})
	if err != nil || text != "summary" {
		t.Fatal(text, err)
	}
}

func TestCodexCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	m := newCodexTestModel(t, server, fixedCodexSource{access: codex.Access{Token: "test"}})
	events, err := m.Stream(ctx, &stream.CallOptions{Messages: []message.Message{message.NewSystemMessage("rules")}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	done := make(chan error, 1)
	go func() {
		var failure error
		for event := range events {
			if e, ok := event.Data.(stream.ErrorEvent); ok {
				failure = e.Error
			}
		}
		done <- failure
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("transport cancellation hung")
	}
}

type countedAbortSource struct{ calls atomic.Int32 }

func (s *countedAbortSource) Access(ctx context.Context) (codex.Access, error) {
	s.calls.Add(1)
	return codex.Access{Token: "test"}, ctx.Err()
}

func TestCodexAlreadyAbortedSkipsCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("aborted call reached network") }))
	defer server.Close()
	source := &countedAbortSource{}
	m := newCodexTestModel(t, server, source)
	abort, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := m.Stream(t.Context(), &stream.CallOptions{AbortSignal: abort, Messages: []message.Message{message.NewSystemMessage("rules")}})
	if !errors.Is(err, context.Canceled) || source.calls.Load() != 0 {
		t.Fatal("already aborted call loaded credentials", err)
	}
}

func drainCodexError(events <-chan stream.Event) error {
	var failure error
	for event := range events {
		if e, ok := event.Data.(stream.ErrorEvent); ok {
			failure = e.Error
		}
	}
	return failure
}

func TestCodexAbortDuringRefresh(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.URL.Path != "/oauth/token" {
			t.Error("unexpected model request after aborted refresh")
			w.WriteHeader(500)
			return
		}
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	shared, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := codex.NewStore(shared)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(t.Context(), func(*codex.Credential) (*codex.Credential, error) {
		return &codex.Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := codex.NewClient(codex.ClientOptions{Store: store, HTTPClient: server.Client(), Issuer: server.URL, ClientID: codex.ClientID, UserAgent: "test"})
	if err != nil {
		t.Fatal(err)
	}
	m := newCodexTestModel(t, server, client)
	abort, cancel := context.WithCancel(t.Context())
	defer cancel()
	events, err := m.Stream(t.Context(), &stream.CallOptions{AbortSignal: abort, Messages: []message.Message{message.NewSystemMessage("rules")}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	done := make(chan error, 1)
	go func() { done <- drainCodexError(events) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("abort left refresh or stream running")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("refresh HTTP request was not canceled")
	}
	credential, err := store.Load(t.Context())
	if err != nil || credential.AccessToken != "old" {
		t.Fatal("interrupted exchange changed store", err)
	}
}

func TestCodexAbortDuringStreamingAndBackpressure(t *testing.T) {
	for _, consume := range []bool{true, false} {
		t.Run(fmt.Sprint(consume), func(t *testing.T) {
			started := make(chan struct{})
			stopped := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				// Exceed both provider and adapter buffers in the non-consuming case.
				for range 600 {
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"text\"}\n\n")
				}
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(stopped)
			}))
			defer server.Close()
			m := newCodexTestModel(t, server, fixedCodexSource{access: codex.Access{Token: "test"}})
			abort, cancel := context.WithCancel(t.Context())
			defer cancel()
			events, err := m.Stream(t.Context(), &stream.CallOptions{AbortSignal: abort, Messages: []message.Message{message.NewSystemMessage("rules")}})
			if err != nil {
				t.Fatal(err)
			}
			<-started
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for len(events) != cap(events) {
				select {
				case <-tick.C:
				case <-deadline.C:
					t.Fatal("stream did not fill adapter buffer")
				}
			}
			if consume {
				for event := range events {
					if _, ok := event.Data.(stream.TextDeltaEvent); ok {
						break
					}
				}
			}
			cancel()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("abort did not close stream HTTP request")
			}
			done := make(chan error, 1)
			go func() { done <- drainCodexError(events) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("abort did not drain and close provider/adapter stream")
			}
		})
	}
}

type contextCaptureSource struct{ contexts chan context.Context }

func (s contextCaptureSource) Access(ctx context.Context) (codex.Access, error) {
	s.contexts <- ctx
	return codex.Access{Token: "test"}, nil
}

func TestCodexNormalCompletionCleansCombinedContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeCodexText(w, "done") }))
	defer server.Close()
	contexts := make(chan context.Context, 1)
	m := newCodexTestModel(t, server, contextCaptureSource{contexts: contexts})
	abort, cancel := context.WithCancel(t.Context())
	defer cancel()
	events, err := m.Stream(t.Context(), &stream.CallOptions{AbortSignal: abort, Messages: []message.Message{message.NewSystemMessage("rules")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := drainCodexError(events); err != nil {
		t.Fatal(err)
	}
	combined := <-contexts
	select {
	case <-combined.Done():
	case <-time.After(time.Second):
		t.Fatal("combined stream context leaked after completion")
	}
	if abort.Err() != nil {
		t.Fatal("adapter canceled caller-owned abort context")
	}
}
