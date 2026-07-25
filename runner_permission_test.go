package sol

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/testutil"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/session"
)

func TestRunnerResolvePermissionSuspensionOrderedBatch(t *testing.T) {
	calls := []stream.ToolCall{
		{ID: "gated-1", Name: "gated1", Input: json.RawMessage(`{"n":1}`)},
		{ID: "safe-1", Name: "safe", Input: json.RawMessage(`{"n":2}`)},
		{ID: "gated-2", Name: "gated2", Input: json.RawMessage(`{"n":3}`)},
		{ID: "tail-1", Name: "tail", Input: json.RawMessage(`{"n":4}`)},
	}
	completed := []stream.ToolResultEvent{{
		ToolCallID: "safe-0",
		ToolName:   "safe",
		Output:     message.TextOutput{Value: "completed before suspension"},
	}}

	tests := []struct {
		name             string
		approved         bool
		wantExecuted     []string
		wantResultIDs    []string
		wantOutcomes     []string
		wantSuspensionID string
	}{
		{
			name:             "approval executes through next gate",
			approved:         true,
			wantExecuted:     []string{"gated1", "safe", "gated2"},
			wantResultIDs:    []string{"gated-1", "safe-1"},
			wantOutcomes:     []string{"success", "success"},
			wantSuspensionID: "gated-2",
		},
		{
			name:          "denial skips entire ordered tail",
			approved:      false,
			wantResultIDs: []string{"gated-1", "safe-1", "gated-2", "tail-1"},
			wantOutcomes:  []string{"denied", "denied", "denied", "denied"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var executed []string
			tools := orderedPermissionTools(&executed)
			runner := NewRunner(RunnerOptions{Agent: testAgent(tools), Quiet: true})
			suspension := &SuspensionContext{
				Reason:           "permission",
				Data:             &bus.ErrPermissionNeeded{Permission: "gated1", ToolCallID: "gated-1"},
				ToolCallID:       "gated-1",
				PendingToolCalls: append([]stream.ToolCall(nil), calls...),
				CompletedResults: append([]stream.ToolResultEvent(nil), completed...),
			}

			var eventOrder []string
			runner.Bus().Subscribe(bus.StreamToolResult, func(event bus.Event) {
				result := event.Properties.(stream.ToolResultEvent)
				eventOrder = append(eventOrder, "result:"+result.ToolCallID)
			})
			runner.Bus().Subscribe(bus.PermissionAsked, func(event bus.Event) {
				asked := event.Properties.(bus.PermissionAskedPayload)
				eventOrder = append(eventOrder, "permission:"+asked.ToolCallID)
			})

			resolution, err := runner.ResolvePermissionSuspension(context.Background(), suspension, tt.approved)
			if err != nil {
				t.Fatalf("ResolvePermissionSuspension() error = %v", err)
			}
			if !reflect.DeepEqual(executed, tt.wantExecuted) {
				t.Errorf("executed = %v, want %v", executed, tt.wantExecuted)
			}
			assertPermissionResults(t, resolution.Results, tt.wantResultIDs, tt.wantOutcomes)

			if tt.wantSuspensionID == "" {
				if resolution.SuspensionContext != nil {
					t.Fatalf("SuspensionContext = %#v, want nil", resolution.SuspensionContext)
				}
				for i, result := range resolution.Results {
					denied := result.Output.(message.ExecutionDeniedOutput)
					wantReason := skippedAfterDeniedReason
					if i == 0 {
						wantReason = permissionDeniedReason
					}
					if denied.Reason != wantReason {
						t.Errorf("result %q reason = %q, want %q", result.ToolCallID, denied.Reason, wantReason)
					}
				}
				return
			}

			next := resolution.SuspensionContext
			if next == nil {
				t.Fatal("SuspensionContext = nil, want later gate")
			}
			if next.ToolCallID != tt.wantSuspensionID {
				t.Errorf("next ToolCallID = %q, want %q", next.ToolCallID, tt.wantSuspensionID)
			}
			if got := toolCallIDs(next.PendingToolCalls); !reflect.DeepEqual(got, []string{"gated-2", "tail-1"}) {
				t.Errorf("next pending IDs = %v", got)
			}
			if got := resultCallIDs(next.CompletedResults); !reflect.DeepEqual(got, []string{"safe-0", "gated-1", "safe-1"}) {
				t.Errorf("next completed IDs = %v", got)
			}
			if !reflect.DeepEqual(eventOrder, []string{"result:gated-1", "result:safe-1", "permission:gated-2"}) {
				t.Errorf("event order = %v", eventOrder)
			}

			final, err := runner.ResolvePermissionSuspension(context.Background(), next, true)
			if err != nil {
				t.Fatalf("resolve final permission: %v", err)
			}
			if final.SuspensionContext != nil {
				t.Fatalf("final SuspensionContext = %#v, want nil", final.SuspensionContext)
			}
			assertPermissionResults(t, final.Results, []string{"gated-2", "tail-1"}, []string{"success", "success"})
			if !reflect.DeepEqual(executed, []string{"gated1", "safe", "gated2", "gated2", "tail"}) {
				t.Errorf("executed after final approval = %v", executed)
			}
		})
	}
}

func TestRunnerResolvePermissionSuspensionStoreOrdering(t *testing.T) {
	tests := []struct {
		name          string
		loadErr       error
		failAppendAt  int
		wantErr       string
		wantOrder     []string
		wantToolCalls []string
	}{
		{
			name:          "load precedes execution and persistence",
			wantOrder:     []string{"load", "execute:gated-1", "append:gated-1", "event:gated-1", "execute:safe-1", "append:safe-1", "event:safe-1"},
			wantToolCalls: []string{"gated-1", "safe-1"},
		},
		{
			name:      "load failure prevents execution",
			loadErr:   errors.New("revision unavailable"),
			wantErr:   "store load before permission resolution: revision unavailable",
			wantOrder: []string{"load"},
		},
		{
			name:          "append failure prevents later execution",
			failAppendAt:  1,
			wantErr:       "store append tool result \"gated-1\": append failed",
			wantOrder:     []string{"load", "execute:gated-1", "append:gated-1"},
			wantToolCalls: []string{"gated-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var order []string
			store := &permissionTestStore{
				order:        &order,
				loadErr:      tt.loadErr,
				failAppendAt: tt.failAppendAt,
				history:      assistantToolCallHistory("gated-1", "gated1", "pending"),
			}
			executor := &permissionTestExecutor{order: &order}
			runner := NewRunner(RunnerOptions{
				Agent:        testAgent(tool.Set{}),
				Executor:     executor,
				SessionStore: store,
				WorkDir:      "/workspace",
				Quiet:        true,
			})
			runner.PermissionManager().SetRules([]bus.PermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}})
			runner.Bus().Subscribe(bus.StreamToolResult, func(event bus.Event) {
				result := event.Properties.(stream.ToolResultEvent)
				order = append(order, "event:"+result.ToolCallID)
			})
			suspension := permissionSuspension("gated-1",
				stream.ToolCall{ID: "gated-1", Name: "gated1"},
				stream.ToolCall{ID: "safe-1", Name: "safe"},
			)

			resolution, err := runner.ResolvePermissionSuspension(context.Background(), suspension, true)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ResolvePermissionSuspension() error = %v", err)
				}
			} else if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if !reflect.DeepEqual(order, tt.wantOrder) {
				t.Errorf("operation order = %v, want %v", order, tt.wantOrder)
			}
			if !reflect.DeepEqual(executor.callIDs, tt.wantToolCalls) {
				t.Errorf("executor calls = %v, want %v", executor.callIDs, tt.wantToolCalls)
			}
			if len(executor.requests) > 0 {
				request := executor.requests[0]
				if request.SessionID == "" {
					t.Error("executor request SessionID is empty")
				}
				if request.WorkDir != "/workspace" {
					t.Errorf("executor request WorkDir = %q", request.WorkDir)
				}
			}
			if tt.failAppendAt == 1 && len(resolution.Results) != 0 {
				t.Errorf("results after failed first append = %d, want 0", len(resolution.Results))
			}
		})
	}
}

func TestRunnerResolvePermissionSuspensionStoreRequiresAssistantCall(t *testing.T) {
	tests := []struct {
		name      string
		history   []session.Message
		wantErr   string
		wantCalls []string
	}{
		{
			name:      "completed assistant call is present",
			history:   assistantToolCallHistory("call-1", "write", "completed"),
			wantCalls: []string{"call-1"},
		},
		{
			name: "synthetic tool result does not replace assistant call",
			history: []session.Message{{
				Role: "tool",
				Parts: []session.Part{{
					Type: "tool",
					Tool: &session.ToolPart{CallID: "call-1", Name: "write", Status: "completed"},
				}},
			}},
			wantErr: "store history does not contain pending assistant tool call \"call-1\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var order []string
			store := &permissionTestStore{order: &order, history: tt.history}
			executor := &permissionTestExecutor{order: &order}
			runner := NewRunner(RunnerOptions{
				Agent:        testAgent(tool.Set{}),
				Executor:     executor,
				SessionStore: store,
				Quiet:        true,
			})

			_, err := runner.ResolvePermissionSuspension(context.Background(), permissionSuspension("call-1",
				stream.ToolCall{ID: "call-1", Name: "write"},
			), true)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ResolvePermissionSuspension() error = %v", err)
				}
			} else if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if !reflect.DeepEqual(executor.callIDs, tt.wantCalls) {
				t.Errorf("executor calls = %v, want %v", executor.callIDs, tt.wantCalls)
			}
		})
	}
}

func TestRunnerResolvePermissionSuspensionCheckpointValidation(t *testing.T) {
	tests := []struct {
		name       string
		suspension *SuspensionContext
		wantID     string
		wantErr    bool
	}{
		{
			name: "checkpoint without explicit ID derives ID from data",
			suspension: &SuspensionContext{
				Reason:           "permission",
				Data:             map[string]any{"permission": "write", "toolCallID": "call-1"},
				PendingToolCalls: []stream.ToolCall{{ID: "call-1", Name: "write"}},
			},
			wantID: "call-1",
		},
		{
			name: "explicit ID must match data",
			suspension: &SuspensionContext{
				Reason:           "permission",
				Data:             map[string]any{"toolCallID": "call-2"},
				ToolCallID:       "call-1",
				PendingToolCalls: []stream.ToolCall{{ID: "call-1", Name: "write"}},
			},
			wantErr: true,
		},
		{
			name: "current call must be first pending",
			suspension: &SuspensionContext{
				Reason:           "permission",
				Data:             map[string]any{"toolCallID": "call-2"},
				PendingToolCalls: []stream.ToolCall{{ID: "call-1"}, {ID: "call-2"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validatePermissionSuspension(tt.suspension)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validatePermissionSuspension() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("validatePermissionSuspension() error = %v", err)
			}
			if got != tt.wantID {
				t.Errorf("current ID = %q, want %q", got, tt.wantID)
			}
		})
	}
}

func TestSuspensionContextFromErrorPopulatesToolCallID(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "permission", err: &bus.ErrPermissionNeeded{Permission: "write"}, reason: "permission"},
		{name: "question", err: &bus.ErrQuestionNeeded{}, reason: "question"},
		{name: "delegated", err: &bus.ErrDelegatedSuspend{Transport: "inprocess"}, reason: "delegated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suspension, ok := suspensionContextFromError(tt.err, []stream.ToolCall{{ID: "call-1"}}, nil)
			if !ok {
				t.Fatal("suspensionContextFromError() did not recognize error")
			}
			if suspension.Reason != tt.reason {
				t.Errorf("reason = %q, want %q", suspension.Reason, tt.reason)
			}
			if suspension.ToolCallID != "call-1" {
				t.Errorf("ToolCallID = %q, want call-1", suspension.ToolCallID)
			}
			dataID, err := suspensionDataToolCallID(suspension.Data)
			if err != nil {
				t.Fatalf("suspensionDataToolCallID() error = %v", err)
			}
			if dataID != "call-1" {
				t.Errorf("data ToolCallID = %q, want call-1", dataID)
			}
		})
	}
}

func TestRunnerResolvePermissionSuspensionNoStoreRunHistory(t *testing.T) {
	model := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("continued", testutil.MockUsage(1, 1)),
	})
	call := stream.ToolCall{ID: "call-1", Name: "write", Input: json.RawMessage(`{}`)}
	runner := NewRunner(RunnerOptions{
		Agent: testAgent(tool.Set{}),
		Model: model,
		InitialMessages: []goai.Message{
			goai.NewUserMessage("write it"),
			goai.NewAssistantMessageWithParts(goai.ToolCallPart{ID: call.ID, Name: call.Name, Input: call.Input}),
		},
		Quiet: true,
	})

	resolution, err := runner.ResolvePermissionSuspension(context.Background(), permissionSuspension(call.ID, call), false)
	if err != nil {
		t.Fatalf("ResolvePermissionSuspension() error = %v", err)
	}
	if len(resolution.Messages) != 1 {
		t.Fatalf("resolution messages = %d, want 1", len(resolution.Messages))
	}
	if len(model.DoStreamCalls) != 0 {
		t.Fatalf("resolver invoked model %d times", len(model.DoStreamCalls))
	}
	if _, err := runner.Run(context.Background(), ""); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.DoStreamCalls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.DoStreamCalls))
	}
	var found bool
	for _, msg := range model.DoStreamCalls[0].Messages {
		if msg.Role != "tool" || !msg.Content.IsMultiPart() {
			continue
		}
		for _, part := range msg.Content.Parts {
			if result, ok := part.(message.ToolResultPart); ok && result.ToolCallID == call.ID {
				found = true
			}
		}
	}
	if !found {
		t.Error("subsequent Run history does not contain resolved tool message")
	}
}

func TestRunnerResolvePermissionSuspensionExecutionOutcomes(t *testing.T) {
	tests := []struct {
		name         string
		firstError   error
		cancel       bool
		wantErr      bool
		wantCalls    []string
		wantOutcomes []string
	}{
		{
			name:         "ordinary error is a result and execution continues",
			firstError:   errors.New("tool failed"),
			wantCalls:    []string{"first", "second"},
			wantOutcomes: []string{"error", "success"},
		},
		{
			name:         "denied execution skips tail",
			firstError:   tool.DeniedError{Reason: "policy denied"},
			wantCalls:    []string{"first"},
			wantOutcomes: []string{"denied", "denied"},
		},
		{
			name: "permission rule denial skips tail",
			firstError: &bus.PermissionDeniedError{
				Permission: "write",
				Pattern:    "/workspace/file",
				Reason:     "denied by rule",
			},
			wantCalls:    []string{"first"},
			wantOutcomes: []string{"denied", "denied"},
		},
		{
			name:       "unrecognized fatal error fails",
			firstError: permissionFatalError{},
			wantErr:    true,
			wantCalls:  []string{"first"},
		},
		{
			name:    "cancelled context prevents execution",
			cancel:  true,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &permissionOutcomeExecutor{firstError: tt.firstError}
			runner := NewRunner(RunnerOptions{
				Agent:    testAgent(tool.Set{}),
				Executor: executor,
				Quiet:    true,
			})
			ctx, cancel := context.WithCancel(context.Background())
			if tt.cancel {
				cancel()
			} else {
				defer cancel()
			}

			resolution, err := runner.ResolvePermissionSuspension(ctx, permissionSuspension("first",
				stream.ToolCall{ID: "first", Name: "first"},
				stream.ToolCall{ID: "second", Name: "second"},
			), true)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ResolvePermissionSuspension() error = nil")
				}
			} else if err != nil {
				t.Fatalf("ResolvePermissionSuspension() error = %v", err)
			}
			if !reflect.DeepEqual(executor.calls, tt.wantCalls) {
				t.Errorf("executor calls = %v, want %v", executor.calls, tt.wantCalls)
			}
			if resolution != nil {
				var outcomes []string
				for _, result := range resolution.Results {
					outcomes = append(outcomes, message.ToolOutcome(result.Output))
				}
				if !reflect.DeepEqual(outcomes, tt.wantOutcomes) {
					t.Errorf("outcomes = %v, want %v", outcomes, tt.wantOutcomes)
				}
			}
		})
	}
}

func TestRunnerResolvePermissionSuspensionPersistsCompletedResultAfterCancellation(t *testing.T) {
	tests := []struct {
		name               string
		cancelAfterExecute bool
		cancelDuringAppend bool
	}{
		{name: "cancel before append", cancelAfterExecute: true},
		{name: "cancel during append", cancelDuringAppend: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &cancellationPermissionStore{
				history:            assistantToolCallHistory("first", "first", "pending"),
				cancel:             cancel,
				cancelDuringAppend: tt.cancelDuringAppend,
			}
			executor := &cancelAfterExecutePermissionExecutor{
				cancel:             cancel,
				cancelAfterExecute: tt.cancelAfterExecute,
			}
			runner := NewRunner(RunnerOptions{
				Agent:        testAgent(tool.Set{}),
				Executor:     executor,
				SessionStore: store,
				Quiet:        true,
			})

			resolution, err := runner.ResolvePermissionSuspension(ctx, permissionSuspension("first",
				stream.ToolCall{ID: "first", Name: "first"},
				stream.ToolCall{ID: "second", Name: "second"},
			), true)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("ResolvePermissionSuspension() error = %v, want context.Canceled", err)
			}
			if !reflect.DeepEqual(executor.calls, []string{"first"}) {
				t.Errorf("executor calls = %v, want [first]", executor.calls)
			}
			if len(store.appended) != 1 || store.appended[0].Parts[0].Tool.CallID != "first" {
				t.Fatalf("appended messages = %#v, want first result", store.appended)
			}
			if store.appendContextErr != nil {
				t.Errorf("append context error = %v, want nil", store.appendContextErr)
			}
			assertPermissionResults(t, resolution.Results, []string{"first"}, []string{"success"})
		})
	}
}

func TestRunnerResolvePermissionSuspensionIgnoresUnrelatedGateEvents(t *testing.T) {
	runner := NewRunner(RunnerOptions{Agent: testAgent(tool.Set{}), Quiet: true})
	executor := &unrelatedGatePermissionExecutor{bus: runner.Bus()}
	runner.executor = executor
	var askedIDs []string
	runner.Bus().Subscribe(bus.PermissionAsked, func(event bus.Event) {
		asked := event.Properties.(bus.PermissionAskedPayload)
		askedIDs = append(askedIDs, asked.ToolCallID)
	})

	resolution, err := runner.ResolvePermissionSuspension(context.Background(), permissionSuspension("first",
		stream.ToolCall{ID: "first", Name: "first"},
		stream.ToolCall{ID: "second", Name: "second"},
	), true)
	if err != nil {
		t.Fatalf("ResolvePermissionSuspension() error = %v", err)
	}
	if resolution.SuspensionContext == nil || resolution.SuspensionContext.ToolCallID != "second" {
		t.Fatalf("SuspensionContext = %#v, want second call", resolution.SuspensionContext)
	}
	if !reflect.DeepEqual(askedIDs, []string{"unrelated", "second"}) {
		t.Errorf("permission event IDs = %v, want [unrelated second]", askedIDs)
	}
}

func orderedPermissionTools(executed *[]string) tool.Set {
	build := func(name string, gated bool) tool.Tool {
		builder := tool.New(name).Description(name).Execute(func(ctx context.Context, _ json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			*executed = append(*executed, name)
			if gated {
				if err := bus.PermissionManagerFromContext(ctx).Ask(ctx, bus.PermissionRequest{
					Permission: name,
					Patterns:   []string{"*"},
					ToolCallID: opts.ToolCallID,
				}); err != nil {
					return tool.Result{}, err
				}
			}
			return tool.Result{Output: name + " complete"}, nil
		})
		return builder.Build()
	}
	return tool.Set{
		"gated1": build("gated1", true),
		"safe":   build("safe", false),
		"gated2": build("gated2", true),
		"tail":   build("tail", false),
	}
}

func permissionSuspension(currentID string, pending ...stream.ToolCall) *SuspensionContext {
	return &SuspensionContext{
		Reason:           "permission",
		Data:             &bus.ErrPermissionNeeded{Permission: "test", ToolCallID: currentID},
		ToolCallID:       currentID,
		PendingToolCalls: pending,
	}
}

func assertPermissionResults(t *testing.T, results []stream.ToolResultEvent, wantIDs, wantOutcomes []string) {
	t.Helper()
	if got := resultCallIDs(results); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("result IDs = %v, want %v", got, wantIDs)
	}
	var outcomes []string
	for _, result := range results {
		outcomes = append(outcomes, message.ToolOutcome(result.Output))
	}
	if !reflect.DeepEqual(outcomes, wantOutcomes) {
		t.Errorf("result outcomes = %v, want %v", outcomes, wantOutcomes)
	}
}

func toolCallIDs(calls []stream.ToolCall) []string {
	ids := make([]string, len(calls))
	for i, call := range calls {
		ids[i] = call.ID
	}
	return ids
}

func resultCallIDs(results []stream.ToolResultEvent) []string {
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.ToolCallID
	}
	return ids
}

type permissionTestStore struct {
	order        *[]string
	loadErr      error
	failAppendAt int
	appendCount  int
	history      []session.Message
}

func (s *permissionTestStore) Load(context.Context) ([]session.Message, error) {
	*s.order = append(*s.order, "load")
	return s.history, s.loadErr
}

func (s *permissionTestStore) Append(_ context.Context, messages []session.Message) error {
	s.appendCount++
	id := messages[0].Parts[0].Tool.CallID
	*s.order = append(*s.order, "append:"+id)
	if s.appendCount == s.failAppendAt {
		return errors.New("append failed")
	}
	return nil
}

func (*permissionTestStore) Compact(context.Context, []session.Message, int) error { return nil }

type permissionTestExecutor struct {
	order    *[]string
	callIDs  []string
	requests []tool.Request
}

func (e *permissionTestExecutor) Execute(_ context.Context, request tool.Request) (tool.Response, error) {
	e.callIDs = append(e.callIDs, request.ToolCallID)
	e.requests = append(e.requests, request)
	*e.order = append(*e.order, "execute:"+request.ToolCallID)
	return tool.Response{Output: request.ToolName + " complete"}, nil
}

func (*permissionTestExecutor) Tools() []tool.Info { return nil }

type permissionOutcomeExecutor struct {
	firstError error
	calls      []string
}

func (e *permissionOutcomeExecutor) Execute(_ context.Context, request tool.Request) (tool.Response, error) {
	e.calls = append(e.calls, request.ToolCallID)
	if request.ToolCallID == "first" && e.firstError != nil {
		return tool.Response{}, e.firstError
	}
	return tool.Response{Output: request.ToolName + " complete"}, nil
}

func (*permissionOutcomeExecutor) Tools() []tool.Info { return nil }

type permissionFatalError struct{}

func (permissionFatalError) Error() string        { return "fatal execution failure" }
func (permissionFatalError) FatalToolError() bool { return true }

func assistantToolCallHistory(callID, name, status string) []session.Message {
	return []session.Message{{
		Role: "assistant",
		Parts: []session.Part{{
			Type: "tool",
			Tool: &session.ToolPart{CallID: callID, Name: name, Status: status},
		}},
	}}
}

type cancellationPermissionStore struct {
	history            []session.Message
	cancel             context.CancelFunc
	cancelDuringAppend bool
	appendContextErr   error
	appended           []session.Message
}

func (s *cancellationPermissionStore) Load(context.Context) ([]session.Message, error) {
	return s.history, nil
}

func (s *cancellationPermissionStore) Append(ctx context.Context, messages []session.Message) error {
	if s.cancelDuringAppend {
		s.cancel()
	}
	s.appendContextErr = ctx.Err()
	s.appended = append(s.appended, messages...)
	return nil
}

func (*cancellationPermissionStore) Compact(context.Context, []session.Message, int) error {
	return nil
}

type cancelAfterExecutePermissionExecutor struct {
	cancel             context.CancelFunc
	cancelAfterExecute bool
	calls              []string
}

func (e *cancelAfterExecutePermissionExecutor) Execute(_ context.Context, request tool.Request) (tool.Response, error) {
	e.calls = append(e.calls, request.ToolCallID)
	if e.cancelAfterExecute {
		e.cancel()
	}
	return tool.Response{Output: request.ToolName + " complete"}, nil
}

func (*cancelAfterExecutePermissionExecutor) Tools() []tool.Info { return nil }

type unrelatedGatePermissionExecutor struct {
	bus *bus.Bus
}

func (e *unrelatedGatePermissionExecutor) Execute(_ context.Context, request tool.Request) (tool.Response, error) {
	if request.ToolCallID == "first" {
		return tool.Response{Output: "first complete"}, nil
	}
	e.bus.Publish(bus.PermissionAsked, bus.PermissionAskedPayload{ToolCallID: "unrelated"})
	return tool.Response{}, &bus.ErrPermissionNeeded{Permission: "write", ToolCallID: request.ToolCallID}
}

func (*unrelatedGatePermissionExecutor) Tools() []tool.Info { return nil }
