package sol

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/testutil"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/session"
)

type deadlineTestModel func(context.Context, *stream.CallOptions) (<-chan stream.Event, error)

func (m deadlineTestModel) ID() string       { return "deadline-test" }
func (m deadlineTestModel) Provider() string { return "test" }
func (m deadlineTestModel) Stream(ctx context.Context, options *stream.CallOptions) (<-chan stream.Event, error) {
	return m(ctx, options)
}

func TestModelProgressHasIndependentAllowance(t *testing.T) {
	for _, kind := range []string{"text", "reasoning", "tool input"} {
		t.Run(kind, func(t *testing.T) {
			model := newMockModel(t, testutil.MockConfig{ID: "progress", Stream: func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
				ch := make(chan stream.Event)
				go func() {
					defer close(ch)
					for i := 0; i < 8; i++ {
						select {
						case <-time.After(15 * time.Millisecond):
						case <-ctx.Done():
							return
						}
						event := stream.Event{Data: stream.TextDeltaEvent{Text: "x"}}
						if kind == "reasoning" {
							event.Data = stream.ReasoningDeltaEvent{ID: "r", Text: "thinking"}
						}
						if kind == "tool input" {
							event.Data = stream.ToolInputDeltaEvent{ID: "t", Delta: "x"}
						}
						select {
						case ch <- event:
						case <-ctx.Done():
							return
						}
					}
					ch <- stream.Event{Data: stream.FinishEvent{FinishReason: stream.FinishReasonStop}}
				}()
				return ch, nil
			}})
			r := NewRunner(RunnerOptions{Agent: testAgent(nil), Model: model, ModelTimeout: time.Second, ModelIdleTimeout: 80 * time.Millisecond, ToolTimeout: 30 * time.Millisecond, Quiet: true})
			if _, err := r.Run(t.Context(), "test"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestToolGetsFreshBudgetAfterModelAndPreviousTool(t *testing.T) {
	model := newMockModel(t, testutil.MockConfig{ID: "slow", Stream: func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
		select {
		case <-time.After(80 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		ch := make(chan stream.Event, 3)
		for _, id := range []string{"first", "second"} {
			ch <- stream.Event{Data: stream.ToolCallEvent{ToolCallID: id, ToolName: "work", Input: json.RawMessage(`{}`)}}
		}
		ch <- stream.Event{Data: stream.FinishEvent{FinishReason: stream.FinishReasonToolCalls}}
		close(ch)
		return ch, nil
	}})
	calls := 0
	tools := tool.Set{"work": tool.New("work").Execute(func(ctx context.Context, _ json.RawMessage, _ tool.CallOptions) (tool.Result, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 100*time.Millisecond {
			t.Error("tool did not receive its own full budget")
		}
		select {
		case <-time.After(80 * time.Millisecond):
			return tool.Result{Output: "done"}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}).Build()}
	a := testAgent(tools)
	a.MaxSteps = 1
	r := NewRunner(RunnerOptions{Agent: a, Model: model, ModelTimeout: time.Second, ModelIdleTimeout: time.Second, ToolTimeout: 150 * time.Millisecond, ToolCallExecutionMode: stream.ToolCallExecutionSync, Quiet: true})
	if _, err := r.Run(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestModelDeadlineCancellationAndProducerSettlement(t *testing.T) {
	for _, kind := range []string{"setup silence", "stream silence", "provider cancellation event", "wall", "caller", "caller deadline"} {
		t.Run(kind, func(t *testing.T) {
			stopped := make(chan struct{})
			entered := make(chan struct{})
			calls := 0
			model := deadlineTestModel(func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
				calls++
				close(entered)
				if kind == "setup silence" {
					<-ctx.Done()
					close(stopped)
					return nil, ctx.Err()
				}
				ch := make(chan stream.Event)
				go func() {
					defer close(ch)
					defer close(stopped)
					tick := time.NewTicker(10 * time.Millisecond)
					defer tick.Stop()
					for {
						select {
						case <-ctx.Done():
							if kind == "provider cancellation event" {
								ch <- stream.Event{Data: stream.ErrorEvent{Error: ctx.Err()}}
							}
							return
						case <-tick.C:
							e := stream.Event{Data: stream.StartEvent{}}
							if kind == "wall" {
								e.Data = stream.ReasoningDeltaEvent{ID: "r", Text: "x"}
							}
							select {
							case ch <- e:
							case <-ctx.Done():
								return
							}
						}
					}
				}()
				return ch, nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "caller deadline" {
				bounded, end := context.WithTimeout(ctx, 20*time.Millisecond)
				defer end()
				ctx = bounded
			}
			if kind == "caller" {
				go func() { <-entered; cancel() }()
			}
			wall, idle := time.Second, 60*time.Millisecond
			if kind == "wall" {
				wall = 100 * time.Millisecond
			}
			r := NewRunner(RunnerOptions{Agent: testAgent(nil), Model: model, ModelTimeout: wall, ModelIdleTimeout: idle, Quiet: true})
			_, err := r.Run(ctx, "test")
			if kind == "caller" {
				if !errors.Is(err, context.Canceled) || errors.Is(err, ErrStepDeadline) {
					t.Fatalf("caller cancellation = %v", err)
				}
			} else if kind == "caller deadline" {
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrStepDeadline) {
					t.Fatalf("caller deadline = %v", err)
				}
			} else if !errors.Is(err, ErrStepDeadline) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline = %v", err)
			}
			select {
			case <-stopped:
			default:
				t.Fatal("producer still active after ownership released")
			}
			if calls != 1 {
				t.Fatal("deadline replayed provider request")
			}
		})
	}
}

type producerSettlementStore struct {
	testStore
	stopped               <-chan struct{}
	checkpointWhileActive bool
}

func (s *producerSettlementStore) Append(ctx context.Context, messages []session.Message) error {
	for _, message := range messages {
		if message.Role == "assistant" {
			select {
			case <-s.stopped:
			default:
				s.checkpointWhileActive = true
			}
		}
	}
	return s.testStore.Append(ctx, messages)
}

func TestModelCancellationJoinsProducerBeforeReturning(t *testing.T) {
	for _, stage := range []string{"setup", "stream"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered, cancelled, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				observed := make(chan struct{})
				model := deadlineTestModel(func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
					ch := make(chan stream.Event)
					go func() {
						defer close(ch)
						defer close(stopped)
						close(entered)
						if stage == "stream" {
							ch <- stream.Event{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "prefix"}}
						}
						<-ctx.Done()
						close(cancelled)
						<-release
						// Settlement can emit output even after cancellation. The wrapper
						// must drain it without requiring a consumer for its own output.
						ch <- stream.Event{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "settled"}}
					}()
					return ch, nil
				})
				store := &producerSettlementStore{stopped: stopped}
				r := NewRunner(RunnerOptions{Agent: testAgent(nil), Model: model, SessionStore: store, ModelLimits: session.ModelLimits{Context: 10000, Input: 8000, Output: 2000}, Quiet: true})
				r.bus.Subscribe(bus.StreamTextDelta, func(bus.Event) { close(observed) })
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := r.Run(ctx, "test"); done <- err }()
				<-entered
				if stage == "stream" {
					<-observed
				}
				cancel()
				<-cancelled
				synctest.Wait()
				select {
				case err := <-done:
					close(release)
					t.Fatalf("released ownership before producer settlement: %v", err)
				default:
				}
				if store.checkpointWhileActive {
					close(release)
					t.Fatal("checkpoint committed before producer settlement")
				}
				close(release)
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation = %v", err)
				}
				select {
				case <-stopped:
				default:
					t.Fatal("producer still active after ownership released")
				}
				if store.checkpointWhileActive {
					t.Fatal("checkpoint committed before producer settlement")
				}
			})
		})
	}
}

func TestModelBackpressureDoesNotCountAsProviderSilence(t *testing.T) {
	model := newMockModel(t, testutil.MockConfig{ID: "backpressure", Stream: func(context.Context, *stream.CallOptions) (<-chan stream.Event, error) {
		ch := make(chan stream.Event, 1)
		ch <- stream.Event{Data: stream.TextDeltaEvent{Text: "ready"}}
		close(ch)
		return ch, nil
	}})
	ctx, stop := context.WithCancelCause(t.Context())
	defer stop(nil)
	m := deadlineModel{Model: model, wall: time.Second, idle: 40 * time.Millisecond, stopStep: stop}
	events, err := m.Stream(ctx, &stream.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	for e := range events {
		if failure, ok := e.Data.(stream.ErrorEvent); ok {
			t.Fatal(failure.Error)
		}
	}
	if ctx.Err() != nil {
		t.Fatal(context.Cause(ctx))
	}
}

func TestModelProducerJoinDoesNotRequireDownstreamConsumption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		produced, cancelled, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		model := deadlineTestModel(func(ctx context.Context, _ *stream.CallOptions) (<-chan stream.Event, error) {
			ch := make(chan stream.Event)
			go func() {
				defer close(ch)
				defer close(stopped)
				ch <- stream.Event{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "prefix"}}
				close(produced)
				<-ctx.Done()
				close(cancelled)
				<-release
				ch <- stream.Event{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "settled"}}
			}()
			return ch, nil
		})
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		bounded := &deadlineModel{Model: model, wall: time.Minute, idle: time.Minute, stopStep: cancel}
		output, err := bounded.Stream(ctx, &stream.CallOptions{})
		if err != nil {
			t.Fatal(err)
		}
		<-produced
		cancel(nil)
		<-cancelled
		joined := make(chan struct{})
		go func() { bounded.producers.Wait(); close(joined) }()
		synctest.Wait()
		select {
		case <-joined:
			close(release)
			t.Fatal("join completed before provider settlement")
		default:
		}
		close(release)
		<-joined
		select {
		case <-stopped:
		default:
			t.Fatal("producer still active after join")
		}
		if _, ok := <-output; ok {
			t.Fatal("unconsumed output remained after cancellation")
		}
	})
}

func TestModelProgressIgnoresFraming(t *testing.T) {
	if modelProgress(stream.Event{Data: stream.StartEvent{}}) || modelProgress(stream.Event{Data: stream.TextDeltaEvent{}}) || modelProgress(stream.Event{Data: stream.RawChunkEvent{}}) || modelProgress(stream.Event{Data: stream.ReasoningEndEvent{}}) {
		t.Fatal("empty framing is not meaningful model progress")
	}
}
