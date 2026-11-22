package sol

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/airlockrun/sol/session"
	"strings"
	"testing"
	"time"

	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/testutil"
	"github.com/airlockrun/goai/tool"
)

func TestToolDeadlineJoinsCallbacksAndStopsBatch(t *testing.T) {
	for _, entry := range []string{"Run", "Continue"} {
		t.Run(entry, func(t *testing.T) {
			var modelDeadline time.Time
			model := newMockModel(t, testutil.MockConfig{ID: "deadline", Stream: func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
				modelDeadline, _ = ctx.Deadline()
				ch := make(chan stream.Event, 4)
				for _, id := range []string{"first", "blocked", "tail"} {
					ch <- stream.Event{Data: stream.ToolCallEvent{ToolCallID: id, ToolName: id, Input: json.RawMessage(`{}`)}}
				}
				ch <- stream.Event{Data: stream.FinishEvent{FinishReason: stream.FinishReasonToolCalls}}
				close(ch)
				return ch, nil
			}})
			cancelled, release := make(chan struct{}), make(chan struct{})
			calls := make(chan string, 3)
			tools := tool.Set{}
			for _, name := range []string{"first", "blocked", "tail"} {
				tools[name] = tool.New(name).Execute(func(ctx context.Context, _ json.RawMessage, _ tool.CallOptions) (tool.Result, error) {
					calls <- name
					deadline, _ := ctx.Deadline()
					if deadline.Equal(modelDeadline) {
						t.Error("tool inherited model deadline")
					}
					if name == "blocked" {
						<-ctx.Done()
						close(cancelled)
						<-release
						return tool.Result{}, ctx.Err()
					}
					return tool.Result{Output: "done"}, nil
				}).Build()
			}
			store := &testStore{}
			r := NewRunner(RunnerOptions{Agent: testAgent(tools), Model: model, SessionStore: store, ToolTimeout: 30 * time.Millisecond, ToolCallExecutionMode: stream.ToolCallExecutionSync, Quiet: true})
			if entry == "Continue" {
				// Initialize without consuming the model fixture.
				r.agent.MaxSteps = 0
				if err := r.initSession(); err != nil {
					t.Fatal(err)
				}
				r.messages = append(r.messages, message.NewSystemMessage("test"))
				r.canContinue = true
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if entry == "Run" {
					_, err = r.Run(t.Context(), "test")
				} else {
					_, err = r.Continue(t.Context(), "test")
				}
				done <- err
			}()
			<-cancelled
			select {
			case err := <-done:
				t.Fatalf("released owner with callback active: %v", err)
			default:
			}
			close(release)
			if err := <-done; !errors.Is(err, ErrStepDeadline) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v", err)
			}
			if len(calls) != 2 {
				t.Fatalf("calls = %d; tail must not dispatch", len(calls))
			}
			var transcript strings.Builder
			for _, msg := range store.messages {
				for _, part := range msg.Parts {
					if part.Tool != nil {
						transcript.WriteString(part.Tool.Output)
					}
				}
			}
			if !strings.Contains(transcript.String(), "Unknown outcome") || !strings.Contains(transcript.String(), "Not dispatched") || strings.Contains(transcript.String(), "Cancelled by user") {
				t.Fatalf("transcript = %s", transcript.String())
			}
			if err := model.Configure(testutil.MockConfig{ID: "deadline", Default: &testutil.MockResponse{Text: "inspected"}}); err != nil {
				t.Fatal(err)
			}
			result, err := r.Continue(t.Context(), "Inspect before retrying")
			if err != nil || result.TotalText != "inspected" {
				t.Fatalf("fresh turn = %+v, %v", result, err)
			}
		})
	}
}

func TestPermissionBatchDeadlineSettlesUnknownAndUndispatched(t *testing.T) {
	tools := tool.Set{"blocked": tool.New("blocked").Execute(func(ctx context.Context, _ json.RawMessage, _ tool.CallOptions) (tool.Result, error) {
		<-ctx.Done()
		return tool.Result{}, ctx.Err()
	}).Build()}
	r := NewRunner(RunnerOptions{Agent: testAgent(tools), ToolTimeout: 5 * time.Millisecond, Quiet: true})
	resolution, err := r.ResolvePermissionSuspension(t.Context(), permissionSuspension("one", stream.ToolCall{ID: "one", Name: "blocked"}, stream.ToolCall{ID: "two", Name: "blocked"}), true)
	if !errors.Is(err, ErrStepDeadline) {
		t.Fatalf("error = %v", err)
	}
	if len(resolution.Results) != 2 || !strings.Contains(message.ToolOutputText(resolution.Results[0].Output), "Unknown outcome") || !strings.Contains(message.ToolOutputText(resolution.Results[1].Output), "Not dispatched") {
		t.Fatalf("results = %+v", resolution.Results)
	}
}

func TestModelDeadlines(t *testing.T) {
	for _, operation := range []string{"model", "compaction"} {
		t.Run(operation, func(t *testing.T) {
			model := newMockModel(t, testutil.MockConfig{ID: "blocked-model", Stream: func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}})
			store := &testStore{messages: []session.Message{{Role: "user", Content: "older"}, {Role: "user", Content: "latest"}}}
			r := NewRunner(RunnerOptions{Agent: testAgent(nil), Model: model, SessionStore: store, ModelTimeout: 5 * time.Millisecond, MaintenanceTimeout: 5 * time.Millisecond, Quiet: true})
			var err error
			if operation == "model" {
				_, err = r.Run(t.Context(), "test")
			} else {
				_, err = r.Compact(t.Context())
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v", err)
			}
			if !errors.Is(err, ErrStepDeadline) {
				t.Fatalf("missing foreground deadline identity: %v", err)
			}
		})
	}
}

func TestCompactionModelBudgets(t *testing.T) {
	for _, name := range []string{"progress", "wall", "idle", "caller"} {
		t.Run(name, func(t *testing.T) {
			model := newMockModel(t, testutil.MockConfig{ID: "summary", Stream: func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
				ch := make(chan stream.Event)
				go func() {
					defer close(ch)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for i := 0; i < 10; i++ {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
						if name == "idle" {
							continue
						}
						select {
						case ch <- stream.Event{Data: stream.TextDeltaEvent{Text: "summary "}}:
						case <-ctx.Done():
							return
						}
					}
					select {
					case ch <- stream.Event{Data: stream.FinishEvent{FinishReason: stream.FinishReasonStop}}:
					case <-ctx.Done():
					}
				}()
				return ch, nil
			}})
			store := &testStore{messages: []session.Message{{Role: "user", Content: "older"}, {Role: "user", Content: "latest"}}}
			wall, idle := time.Second, time.Second
			if name == "wall" {
				wall = 40 * time.Millisecond
			}
			if name == "idle" {
				idle = 40 * time.Millisecond
			}
			ctx := t.Context()
			if name == "caller" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 40*time.Millisecond)
				defer cancel()
			}
			r := NewRunner(RunnerOptions{Agent: testAgent(nil), Model: model, SessionStore: store, ModelTimeout: wall, ModelIdleTimeout: idle, MaintenanceTimeout: time.Millisecond, Quiet: true})
			_, err := r.Compact(ctx)
			if name == "progress" {
				if err != nil {
					t.Fatal(err)
				}
				if !store.messages[0].Summary {
					t.Fatal("summary not persisted")
				}
				return
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v", err)
			}
			if name != "caller" && !errors.Is(err, ErrStepDeadline) {
				t.Fatalf("missing model deadline identity: %v", err)
			}
			if len(store.messages) != 2 || store.messages[0].Content != "older" {
				t.Fatal("failed compaction changed history")
			}
		})
	}
}

type deadlineCompactionStore struct {
	testStore
}

func (s *deadlineCompactionStore) Compact(ctx context.Context, _ []session.Message, _ int) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestCompactionCheckpointDeadline(t *testing.T) {
	store := &deadlineCompactionStore{testStore: testStore{messages: []session.Message{{Role: "user", Content: "older"}, {Role: "user", Content: "latest"}}}}
	model := newMockModel(t, testutil.MockConfig{ID: "summary", Default: &testutil.MockResponse{Text: "summary"}})
	r := NewRunner(RunnerOptions{Agent: testAgent(nil), Model: model, SessionStore: store, MaintenanceTimeout: 10 * time.Millisecond, Quiet: true})
	if _, err := r.Compact(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("checkpoint error = %v", err)
	}
	if r.canContinue {
		t.Fatal("runner may continue after checkpoint failure")
	}
	if store.messages[0].Content != "older" {
		t.Fatal("failed checkpoint replaced durable history")
	}
}

func TestCompactionCancellationJoinsProducer(t *testing.T) {
	entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	model := deadlineTestModel(func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
		ch := make(chan stream.Event)
		go func() {
			defer close(ch)
			defer close(stopped)
			close(entered)
			<-ctx.Done()
			<-release
		}()
		return ch, nil
	})
	store := &testStore{messages: []session.Message{{Role: "user", Content: "older"}, {Role: "user", Content: "latest"}}}
	r := NewRunner(RunnerOptions{Agent: testAgent(nil), Model: model, SessionStore: store, Quiet: true})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Compact(ctx); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("released ownership before producer settled: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compaction did not settle")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("producer still running")
	}
	if store.messages[0].Content != "older" {
		t.Fatal("cancelled compaction changed history")
	}
}
