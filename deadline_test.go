package sol

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/testutil"
	"github.com/airlockrun/goai/tool"
)

func TestStepDeadlineOwnsWholeBatchAndJoinsCallbacks(t *testing.T) {
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
					if !deadline.Equal(modelDeadline) {
						t.Error("tool reset the model step deadline")
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
			r := NewRunner(RunnerOptions{Agent: testAgent(tools), Model: model, SessionStore: store, StepTimeout: 30 * time.Millisecond, ToolCallExecutionMode: stream.ToolCallExecutionSync, Quiet: true})
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
	r := NewRunner(RunnerOptions{Agent: testAgent(tools), StepTimeout: 5 * time.Millisecond, Quiet: true})
	resolution, err := r.ResolvePermissionSuspension(t.Context(), permissionSuspension("one", stream.ToolCall{ID: "one", Name: "blocked"}, stream.ToolCall{ID: "two", Name: "blocked"}), true)
	if !errors.Is(err, ErrStepDeadline) {
		t.Fatalf("error = %v", err)
	}
	if len(resolution.Results) != 2 || !strings.Contains(message.ToolOutputText(resolution.Results[0].Output), "Unknown outcome") || !strings.Contains(message.ToolOutputText(resolution.Results[1].Output), "Not dispatched") {
		t.Fatalf("results = %+v", resolution.Results)
	}
}

func TestModelAndMaintenanceDeadlines(t *testing.T) {
	for _, operation := range []string{"model", "compaction"} {
		t.Run(operation, func(t *testing.T) {
			model := newMockModel(t, testutil.MockConfig{ID: "blocked-model", Stream: func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}})
			store := &testStore{}
			r := NewRunner(RunnerOptions{Agent: testAgent(nil), Model: model, SessionStore: store, StepTimeout: 5 * time.Millisecond, MaintenanceTimeout: 5 * time.Millisecond, Quiet: true})
			var err error
			if operation == "model" {
				_, err = r.Run(t.Context(), "test")
			} else {
				_, err = r.Compact(t.Context())
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v", err)
			}
			if operation == "model" && !errors.Is(err, ErrStepDeadline) {
				t.Fatalf("missing foreground deadline identity: %v", err)
			}
		})
	}
}
