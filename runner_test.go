package sol

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/testutil"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/agent"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/session"
	"github.com/airlockrun/sol/tools"
)

// testStore is a simple in-memory session store that records appended messages.
type testStore struct {
	messages    []session.Message
	tokensFreed int
}

func (s *testStore) Load(context.Context) ([]session.Message, error) {
	return s.messages, nil
}

func (s *testStore) Append(_ context.Context, msgs []session.Message) error {
	s.messages = append(s.messages, msgs...)
	return nil
}

func (s *testStore) Compact(_ context.Context, summary []session.Message, tokensFreed int) error {
	s.messages = make([]session.Message, len(summary))
	copy(s.messages, summary)
	s.tokensFreed = tokensFreed
	return nil
}

// testAgent returns a minimal build agent for testing with the given tools.
func testAgent(ts tool.Set) *agent.Agent {
	return &agent.Agent{
		Name:     "build",
		MaxSteps: 10,
		Tools:    ts,
	}
}

func TestRunner_AgentToolSets(t *testing.T) {
	// Explore agent should only have specific tools
	exploreAgent := agent.NewExploreAgent("gpt-4o")

	for _, name := range []string{"read", "glob", "grep", "bash"} {
		if _, ok := exploreAgent.Tools[name]; !ok {
			t.Errorf("explore agent should have %s tool", name)
		}
	}
	for _, name := range []string{"write", "edit", "task"} {
		if _, ok := exploreAgent.Tools[name]; ok {
			t.Errorf("explore agent should not have %s tool", name)
		}
	}
}

func TestRunner_AgentToolSets_BuildAgent(t *testing.T) {
	buildAgent := agent.NewBuildAgent("gpt-4o")

	expectedTools := []string{"read", "glob", "grep", "write", "edit", "bash", "task"}
	for _, toolName := range expectedTools {
		if _, ok := buildAgent.Tools[toolName]; !ok {
			t.Errorf("build agent should have %s tool", toolName)
		}
	}
}

func TestRunner_AgentToolSets_GeneralAgent(t *testing.T) {
	generalAgent := agent.NewGeneralAgent("gpt-4o")

	for _, name := range []string{"read", "write", "edit", "bash", "task"} {
		if _, ok := generalAgent.Tools[name]; !ok {
			t.Errorf("general agent should have %s tool", name)
		}
	}
	for _, name := range []string{"todoread", "todowrite"} {
		if _, ok := generalAgent.Tools[name]; ok {
			t.Errorf("general agent should not have %s tool (denied)", name)
		}
	}
}

func TestRunner_AgentToolSets_PlanAgent(t *testing.T) {
	planAgent := agent.NewPlanAgent("gpt-4o")

	if _, ok := planAgent.Tools["read"]; !ok {
		t.Error("plan agent should have read tool")
	}
	if _, ok := planAgent.Tools["glob"]; !ok {
		t.Error("plan agent should have glob tool")
	}
	if _, ok := planAgent.Tools["grep"]; !ok {
		t.Error("plan agent should have grep tool")
	}
	if _, ok := planAgent.Tools["write"]; ok {
		t.Error("plan agent should not have write tool")
	}
	if _, ok := planAgent.Tools["bash"]; ok {
		t.Error("plan agent should not have bash tool")
	}
}

func TestNewRunner_Options(t *testing.T) {
	a := testAgent(tools.CreateToolSetForModel(""))
	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("hi", testutil.MockUsage(1, 1)),
	})

	runner := NewRunner(RunnerOptions{
		Agent: a,
		Model: mockModel,
	})

	if runner.agent.Name != "build" {
		t.Errorf("expected agent name 'build', got %s", runner.agent.Name)
	}
}

func TestRunner_Run_CompletedStatus(t *testing.T) {
	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("Hello!", testutil.MockUsage(10, 5)),
	})

	runner := NewRunner(RunnerOptions{
		Agent: testAgent(tool.Set{}),
		Model: mockModel,
		Quiet: true,
	})

	result, err := runner.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != RunCompleted {
		t.Errorf("expected RunCompleted, got %s", result.Status)
	}
	if result.TotalText != "Hello!" {
		t.Errorf("expected 'Hello!', got %q", result.TotalText)
	}
	if len(result.Messages) == 0 {
		t.Fatal("Messages should always be populated")
	}
	if result.SuspensionContext != nil {
		t.Error("SuspensionContext should be nil on completion")
	}

	// Messages should contain: system, user("hi"), assistant("Hello!")
	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "system" {
		t.Errorf("msg[0] role = %s, want system", result.Messages[0].Role)
	}
	if result.Messages[1].Role != "user" {
		t.Errorf("msg[1] role = %s, want user", result.Messages[1].Role)
	}
	if result.Messages[2].Role != "assistant" {
		t.Errorf("msg[2] role = %s, want assistant", result.Messages[2].Role)
	}
}

func TestRunner_RunWithInitialMessages_ThenContinue(t *testing.T) {
	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponses: [][]stream.Event{
			testutil.MockTextResponse("response 1", testutil.MockUsage(10, 5)),
			testutil.MockTextResponse("response 2", testutil.MockUsage(15, 8)),
		},
	})

	// initialMessages should not include system prompt — runner always prepends its own
	initialMessages := []goai.Message{
		goai.NewUserMessage("what is 2+2?"),
		goai.NewAssistantMessage("4"),
	}

	runner := NewRunner(RunnerOptions{
		Agent:           testAgent(tool.Set{}),
		Model:           mockModel,
		InitialMessages: initialMessages,
		Quiet:           true,
	})

	result1, err := runner.Run(context.Background(), "and 3+3?")
	if err != nil {
		t.Fatal(err)
	}

	if result1.Status != RunCompleted {
		t.Errorf("expected RunCompleted, got %s", result1.Status)
	}

	// Messages: system(prepended) + initial(2) + user("and 3+3?") + assistant("response 1") = 5
	if len(result1.Messages) != 5 {
		t.Fatalf("expected 5 messages after Run, got %d", len(result1.Messages))
	}
	if result1.Messages[0].Role != "system" {
		t.Errorf("msg[0] should be system, got %s", result1.Messages[0].Role)
	}
	if result1.Messages[3].Role != "user" {
		t.Errorf("msg[3] should be user, got %s", result1.Messages[3].Role)
	}
	if result1.Messages[3].Content.Text != "and 3+3?" {
		t.Errorf("msg[3] content = %q", result1.Messages[3].Content.Text)
	}
	if result1.Messages[4].Content.Text != "response 1" {
		t.Errorf("msg[4] content = %q", result1.Messages[4].Content.Text)
	}

	// Continue the conversation
	result2, err := runner.Continue(context.Background(), "and 4+4?")
	if err != nil {
		t.Fatal(err)
	}

	if result2.Status != RunCompleted {
		t.Errorf("expected RunCompleted, got %s", result2.Status)
	}

	// Messages: system + initial(2) + user + assistant + user("and 4+4?") + assistant("response 2") = 7
	if len(result2.Messages) != 7 {
		t.Fatalf("expected 7 messages after Continue, got %d", len(result2.Messages))
	}
	if result2.Messages[5].Role != "user" {
		t.Errorf("msg[5] should be user, got %s", result2.Messages[5].Role)
	}
	if result2.Messages[5].Content.Text != "and 4+4?" {
		t.Errorf("msg[5] content = %q", result2.Messages[5].Content.Text)
	}
	if result2.Messages[6].Content.Text != "response 2" {
		t.Errorf("msg[6] content = %q", result2.Messages[6].Content.Text)
	}
}

func TestRunner_RunWithInitialMessages_EmptyPrompt(t *testing.T) {
	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("resumed!", testutil.MockUsage(10, 5)),
	})

	// initialMessages should not include system prompt — runner always prepends its own
	initialMessages := []goai.Message{
		goai.NewUserMessage("do something"),
		goai.NewAssistantMessage("ok let me help"),
	}

	runner := NewRunner(RunnerOptions{
		Agent:           testAgent(tool.Set{}),
		Model:           mockModel,
		InitialMessages: initialMessages,
		Quiet:           true,
	})

	// Run with empty prompt (resume from checkpoint)
	result, err := runner.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	// Should NOT have an extra empty user message
	// Messages: system(prepended) + initial(2) + assistant("resumed!") = 4
	if len(result.Messages) != 4 {
		t.Fatalf("expected 4 messages (no empty user msg appended), got %d", len(result.Messages))
	}
}

func TestRunner_SuspensionOnPermissionNeeded(t *testing.T) {
	permTool := tool.New("perm_tool").
		Description("Tool that asks permission").
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			pmFromCtx := bus.PermissionManagerFromContext(ctx)
			err := pmFromCtx.Ask(ctx, bus.PermissionRequest{
				SessionID:  "test-session",
				Permission: "dangerous",
				Patterns:   []string{"*"},
				ToolCallID: opts.ToolCallID,
			})
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Output: "done"}, nil
		}).Build()

	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockToolCallResponse("call_1", "perm_tool", map[string]string{}, testutil.MockUsage(10, 5)),
	})

	runner := NewRunner(RunnerOptions{
		Agent: testAgent(tool.Set{"perm_tool": permTool}),
		Model: mockModel,
		Quiet: true,
	})

	result, err := runner.Run(context.Background(), "do something dangerous")
	if err != nil {
		t.Fatalf("suspension should not return error, got: %v", err)
	}

	if result.Status != RunSuspended {
		t.Errorf("expected RunSuspended, got %s", result.Status)
	}
	if result.SuspensionContext == nil {
		t.Fatal("SuspensionContext should be non-nil")
	}
	if result.SuspensionContext.Reason != "permission" {
		t.Errorf("expected reason 'permission', got %q", result.SuspensionContext.Reason)
	}
	if len(result.SuspensionContext.PendingToolCalls) != 1 {
		t.Errorf("expected 1 pending tool call, got %d", len(result.SuspensionContext.PendingToolCalls))
	}
	if len(result.Messages) == 0 {
		t.Error("Messages should be populated on suspension")
	}
}

func TestRunner_CheckpointResumeRoundTrip(t *testing.T) {
	permTool := tool.New("perm_tool").
		Description("Tool that asks permission").
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			pmFromCtx := bus.PermissionManagerFromContext(ctx)
			err := pmFromCtx.Ask(ctx, bus.PermissionRequest{
				SessionID:  "test-session",
				Permission: "dangerous",
				Patterns:   []string{"*"},
				ToolCallID: opts.ToolCallID,
			})
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Output: "done"}, nil
		}).Build()

	mockModel1 := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockToolCallResponse("call_1", "perm_tool", map[string]string{}, testutil.MockUsage(10, 5)),
	})

	runner1 := NewRunner(RunnerOptions{
		Agent: testAgent(tool.Set{"perm_tool": permTool}),
		Model: mockModel1,
		Quiet: true,
	})

	result1, err := runner1.Run(context.Background(), "do something dangerous")
	if err != nil {
		t.Fatalf("suspension should not return error: %v", err)
	}
	if result1.Status != RunSuspended {
		t.Fatalf("expected RunSuspended, got %s", result1.Status)
	}

	// Serialize the checkpoint to JSON (simulating DB persist)
	checkpointJSON, err := json.Marshal(result1)
	if err != nil {
		t.Fatalf("failed to serialize RunResult: %v", err)
	}

	// Deserialize (simulating DB load)
	var checkpoint RunResult
	if err := json.Unmarshal(checkpointJSON, &checkpoint); err != nil {
		t.Fatalf("failed to deserialize RunResult: %v", err)
	}

	if checkpoint.Status != RunSuspended {
		t.Errorf("deserialized status = %s, want suspended", checkpoint.Status)
	}
	if checkpoint.SuspensionContext == nil || checkpoint.SuspensionContext.Reason != "permission" {
		t.Error("deserialized SuspensionContext mismatch")
	}
	if len(checkpoint.Messages) == 0 {
		t.Fatal("deserialized Messages should be populated")
	}

	// Resume with a new runner using the checkpointed messages
	mockModel2 := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("resumed and done!", testutil.MockUsage(15, 10)),
	})

	runner2 := NewRunner(RunnerOptions{
		Agent:           testAgent(tool.Set{}),
		Model:           mockModel2,
		InitialMessages: checkpoint.Messages,
		Quiet:           true,
	})

	result2, err := runner2.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}

	if result2.Status != RunCompleted {
		t.Errorf("expected RunCompleted after resume, got %s", result2.Status)
	}
	if result2.TotalText != "resumed and done!" {
		t.Errorf("expected 'resumed and done!', got %q", result2.TotalText)
	}

	if len(result2.Messages) <= len(checkpoint.Messages) {
		t.Errorf("resumed messages (%d) should be longer than checkpoint (%d)",
			len(result2.Messages), len(checkpoint.Messages))
	}
}

func TestRunner_AllowAllRules_NoSuspension(t *testing.T) {
	permTool := tool.New("perm_tool").
		Description("Tool that asks permission").
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			pmFromCtx := bus.PermissionManagerFromContext(ctx)
			err := pmFromCtx.Ask(ctx, bus.PermissionRequest{
				SessionID:  "test-session",
				Permission: "dangerous",
				Patterns:   []string{"*"},
			})
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Output: "done"}, nil
		}).Build()

	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponses: [][]stream.Event{
			testutil.MockToolCallResponse("call_1", "perm_tool", map[string]string{}, testutil.MockUsage(10, 5)),
			testutil.MockTextResponse("completed!", testutil.MockUsage(5, 3)),
		},
	})

	runner := NewRunner(RunnerOptions{
		Agent: testAgent(tool.Set{"perm_tool": permTool}),
		Model: mockModel,
		Quiet: true,
	})
	runner.PermissionManager().SetRules([]bus.PermissionRule{
		{Permission: "*", Pattern: "*", Action: "allow"},
	})

	result, err := runner.Run(context.Background(), "do something dangerous")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != RunCompleted {
		t.Errorf("expected RunCompleted, got %s", result.Status)
	}
	if result.SuspensionContext != nil {
		t.Error("SuspensionContext should be nil when completed")
	}
}

func TestRunner_StreamEventsOnBus(t *testing.T) {
	echoTool := tool.New("echo").
		Description("Echo tool").
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			return tool.Result{Output: "echoed"}, nil
		}).Build()

	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponses: [][]stream.Event{
			testutil.MockToolCallResponse("call_1", "echo", map[string]string{}, testutil.MockUsage(10, 5)),
			testutil.MockTextResponse("done!", testutil.MockUsage(5, 3)),
		},
	})

	runner := NewRunner(RunnerOptions{
		Agent: testAgent(tool.Set{"echo": echoTool}),
		Model: mockModel,
		Quiet: true,
	})
	runner.PermissionManager().SetRules([]bus.PermissionRule{
		{Permission: "*", Pattern: "*", Action: "allow"},
	})

	var textDeltas, toolCalls, toolResults, stepCompletes int
	runner.Bus().Subscribe(bus.StreamTextDelta, func(e bus.Event) { textDeltas++ })
	runner.Bus().Subscribe(bus.StreamToolCall, func(e bus.Event) { toolCalls++ })
	runner.Bus().Subscribe(bus.StreamToolResult, func(e bus.Event) { toolResults++ })
	runner.Bus().Subscribe(bus.StreamStepComplete, func(e bus.Event) { stepCompletes++ })

	result, err := runner.Run(context.Background(), "echo something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != RunCompleted {
		t.Fatalf("expected RunCompleted, got %s", result.Status)
	}

	if toolCalls < 1 {
		t.Errorf("expected at least 1 StreamToolCall event, got %d", toolCalls)
	}
	if toolResults < 1 {
		t.Errorf("expected at least 1 StreamToolResult event, got %d", toolResults)
	}
	if textDeltas < 1 {
		t.Errorf("expected at least 1 StreamTextDelta event, got %d", textDeltas)
	}
	if stepCompletes < 2 {
		t.Errorf("expected at least 2 StreamStepComplete events, got %d", stepCompletes)
	}
}

func TestRunResult_JSONRoundTrip(t *testing.T) {
	result := &RunResult{
		AgentName: "build",
		TotalText: "I'll help you",
		Status:    RunCompleted,
		Messages: []goai.Message{
			goai.NewSystemMessage("you are helpful"),
			goai.NewUserMessage("hello"),
			goai.NewAssistantMessage("I'll help you"),
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	var got RunResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.AgentName != "build" {
		t.Errorf("agentName = %q", got.AgentName)
	}
	if got.Status != RunCompleted {
		t.Errorf("status = %s", got.Status)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got.Messages))
	}
	if got.Messages[2].Content.Text != "I'll help you" {
		t.Errorf("messages[2] = %q", got.Messages[2].Content.Text)
	}
}

func TestRunResult_SuspendedJSON(t *testing.T) {
	result := &RunResult{
		AgentName: "build",
		Status:    RunSuspended,
		Messages: []goai.Message{
			goai.NewSystemMessage("sys"),
			goai.NewUserMessage("do something"),
		},
		SuspensionContext: &SuspensionContext{
			Reason: "permission",
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	var got RunResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.Status != RunSuspended {
		t.Errorf("status = %s", got.Status)
	}
	if got.SuspensionContext == nil {
		t.Fatal("SuspensionContext should be non-nil")
	}
	if got.SuspensionContext.Reason != "permission" {
		t.Errorf("reason = %q", got.SuspensionContext.Reason)
	}
}

func TestRunner_InterruptResumeEquivalence(t *testing.T) {
	allowAll := []bus.PermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}}

	t.Run("permission", func(t *testing.T) {
		greetTool := tool.New("greet").
			Description("Greets someone").
			Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
				pm := bus.PermissionManagerFromContext(ctx)
				if err := pm.Ask(ctx, bus.PermissionRequest{
					Permission: "greet",
					Patterns:   []string{"*"},
					ToolCallID: opts.ToolCallID,
				}); err != nil {
					return tool.Result{}, err
				}
				return tool.Result{Output: "Hello, world!"}, nil
			}).Build()
		toolSet := tool.Set{"greet": greetTool}

		// === Path A: auto-approve, uninterrupted ===
		mockA := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponses: [][]stream.Event{
				testutil.MockToolCallResponse("call_1", "greet", map[string]string{}, testutil.MockUsage(10, 5)),
				testutil.MockTextResponse("Greeting done!", testutil.MockUsage(15, 8)),
			},
		})
		runnerA := NewRunner(RunnerOptions{Agent: testAgent(toolSet), Model: mockA, Quiet: true})
		runnerA.PermissionManager().SetRules(allowAll)

		resultA, err := runnerA.Run(context.Background(), "greet the world")
		if err != nil {
			t.Fatalf("Path A: unexpected error: %v", err)
		}
		if resultA.Status != RunCompleted {
			t.Fatalf("Path A: expected RunCompleted, got %s", resultA.Status)
		}

		// === Path B: interrupt → serialize → resume ===

		// Phase 1: Run with no rules → suspension
		mock2a := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponse: testutil.MockToolCallResponse("call_1", "greet", map[string]string{}, testutil.MockUsage(10, 5)),
		})
		runner2a := NewRunner(RunnerOptions{Agent: testAgent(toolSet), Model: mock2a, Quiet: true})

		result2a, err := runner2a.Run(context.Background(), "greet the world")
		if err != nil {
			t.Fatalf("Path B phase 1: unexpected error: %v", err)
		}
		if result2a.Status != RunSuspended {
			t.Fatalf("Path B phase 1: expected RunSuspended, got %s", result2a.Status)
		}
		if result2a.SuspensionContext == nil || result2a.SuspensionContext.Reason != "permission" {
			t.Fatal("Path B phase 1: expected permission suspension")
		}
		if len(result2a.SuspensionContext.PendingToolCalls) != 1 {
			t.Fatalf("Path B phase 1: expected 1 pending tool call, got %d", len(result2a.SuspensionContext.PendingToolCalls))
		}

		// Phase 2: Serialize → deserialize
		data, err := json.Marshal(result2a)
		if err != nil {
			t.Fatalf("Path B phase 2: marshal error: %v", err)
		}
		var checkpoint RunResult
		if err := json.Unmarshal(data, &checkpoint); err != nil {
			t.Fatalf("Path B phase 2: unmarshal error: %v", err)
		}

		// Phase 3: Resume with allow-all rules
		mock2b := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponses: [][]stream.Event{
				testutil.MockToolCallResponse("call_1", "greet", map[string]string{}, testutil.MockUsage(10, 5)),
				testutil.MockTextResponse("Greeting done!", testutil.MockUsage(15, 8)),
			},
		})
		runner2b := NewRunner(RunnerOptions{
			Agent:           testAgent(toolSet),
			Model:           mock2b,
			InitialMessages: checkpoint.Messages,
			Quiet:           true,
		})
		runner2b.PermissionManager().SetRules(allowAll)

		resultB, err := runner2b.Run(context.Background(), "")
		if err != nil {
			t.Fatalf("Path B phase 3: unexpected error: %v", err)
		}
		if resultB.Status != RunCompleted {
			t.Fatalf("Path B phase 3: expected RunCompleted, got %s", resultB.Status)
		}

		// === Compare ===
		if resultA.TotalText != resultB.TotalText {
			t.Errorf("TotalText mismatch: Path A=%q, Path B=%q", resultA.TotalText, resultB.TotalText)
		}
		if resultA.TotalText != "Greeting done!" {
			t.Errorf("expected TotalText 'Greeting done!', got %q", resultA.TotalText)
		}

		foundToolResultA := false
		for _, step := range resultA.Steps {
			for _, tr := range step.ToolResults {
				if tr.ToolName == "greet" && tr.Output.Output == "Hello, world!" {
					foundToolResultA = true
				}
			}
		}
		foundToolResultB := false
		for _, step := range resultB.Steps {
			for _, tr := range step.ToolResults {
				if tr.ToolName == "greet" && tr.Output.Output == "Hello, world!" {
					foundToolResultB = true
				}
			}
		}
		if !foundToolResultA {
			t.Error("Path A: missing tool result 'Hello, world!' from greet")
		}
		if !foundToolResultB {
			t.Error("Path B: missing tool result 'Hello, world!' from greet")
		}

		if len(resultB.Messages) <= len(checkpoint.Messages) {
			t.Errorf("resumed messages (%d) should be longer than checkpoint (%d)",
				len(resultB.Messages), len(checkpoint.Messages))
		}
	})

	t.Run("question", func(t *testing.T) {
		askTool := tool.New("ask_color").
			Description("Asks favorite color").
			Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
				qm := bus.QuestionManagerFromContext(ctx)
				answers, err := qm.Ask(ctx, bus.AskInput{
					SessionID: "test",
					Questions: []bus.QuestionInfo{
						{Question: "Favorite color?", Header: "Color", Options: []bus.QuestionOption{
							{Label: "Red"}, {Label: "Blue"}, {Label: "Green"},
						}},
					},
					Tool: &bus.ToolContext{CallID: opts.ToolCallID},
				})
				if err != nil {
					return tool.Result{}, err
				}
				answer := "unknown"
				if len(answers) > 0 && len(answers[0]) > 0 {
					answer = answers[0][0]
				}
				return tool.Result{Output: "Color: " + answer}, nil
			}).Build()
		toolSet := tool.Set{"ask_color": askTool}

		// === Path A: pre-loaded answer, uninterrupted ===
		mockA := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponses: [][]stream.Event{
				testutil.MockToolCallResponse("call_1", "ask_color", map[string]string{}, testutil.MockUsage(10, 5)),
				testutil.MockTextResponse("Your color is noted!", testutil.MockUsage(15, 8)),
			},
		})
		runnerA := NewRunner(RunnerOptions{Agent: testAgent(toolSet), Model: mockA, Quiet: true})
		runnerA.PermissionManager().SetRules(allowAll)
		runnerA.QuestionManager().PushAnswers([][]string{{"Blue"}})

		resultA, err := runnerA.Run(context.Background(), "ask about colors")
		if err != nil {
			t.Fatalf("Path A: unexpected error: %v", err)
		}
		if resultA.Status != RunCompleted {
			t.Fatalf("Path A: expected RunCompleted, got %s", resultA.Status)
		}

		// === Path B: no answer → suspension → resume with answer ===

		mock2a := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponse: testutil.MockToolCallResponse("call_1", "ask_color", map[string]string{}, testutil.MockUsage(10, 5)),
		})
		runner2a := NewRunner(RunnerOptions{Agent: testAgent(toolSet), Model: mock2a, Quiet: true})
		runner2a.PermissionManager().SetRules(allowAll)

		result2a, err := runner2a.Run(context.Background(), "ask about colors")
		if err != nil {
			t.Fatalf("Path B phase 1: unexpected error: %v", err)
		}
		if result2a.Status != RunSuspended {
			t.Fatalf("Path B phase 1: expected RunSuspended, got %s", result2a.Status)
		}
		if result2a.SuspensionContext == nil || result2a.SuspensionContext.Reason != "question" {
			t.Fatal("Path B phase 1: expected question suspension")
		}

		data, err := json.Marshal(result2a)
		if err != nil {
			t.Fatalf("Path B phase 2: marshal error: %v", err)
		}
		var checkpoint RunResult
		if err := json.Unmarshal(data, &checkpoint); err != nil {
			t.Fatalf("Path B phase 2: unmarshal error: %v", err)
		}

		mock2b := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponses: [][]stream.Event{
				testutil.MockToolCallResponse("call_1", "ask_color", map[string]string{}, testutil.MockUsage(10, 5)),
				testutil.MockTextResponse("Your color is noted!", testutil.MockUsage(15, 8)),
			},
		})
		runner2b := NewRunner(RunnerOptions{
			Agent:           testAgent(toolSet),
			Model:           mock2b,
			InitialMessages: checkpoint.Messages,
			Quiet:           true,
		})
		runner2b.PermissionManager().SetRules(allowAll)
		runner2b.QuestionManager().PushAnswers([][]string{{"Blue"}})

		resultB, err := runner2b.Run(context.Background(), "")
		if err != nil {
			t.Fatalf("Path B phase 3: unexpected error: %v", err)
		}
		if resultB.Status != RunCompleted {
			t.Fatalf("Path B phase 3: expected RunCompleted, got %s", resultB.Status)
		}

		// === Compare ===
		if resultA.TotalText != resultB.TotalText {
			t.Errorf("TotalText mismatch: Path A=%q, Path B=%q", resultA.TotalText, resultB.TotalText)
		}
		if resultA.TotalText != "Your color is noted!" {
			t.Errorf("expected TotalText 'Your color is noted!', got %q", resultA.TotalText)
		}

		foundA := false
		for _, step := range resultA.Steps {
			for _, tr := range step.ToolResults {
				if tr.ToolName == "ask_color" && tr.Output.Output == "Color: Blue" {
					foundA = true
				}
			}
		}
		foundB := false
		for _, step := range resultB.Steps {
			for _, tr := range step.ToolResults {
				if tr.ToolName == "ask_color" && tr.Output.Output == "Color: Blue" {
					foundB = true
				}
			}
		}
		if !foundA {
			t.Error("Path A: missing tool result 'Color: Blue'")
		}
		if !foundB {
			t.Error("Path B: missing tool result 'Color: Blue'")
		}
	})

	t.Run("partial_tools", func(t *testing.T) {
		safeEcho := tool.New("safe_echo").
			Description("Echo without permission").
			Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
				return tool.Result{Output: "echoed!"}, nil
			}).Build()

		permGreet := tool.New("perm_greet").
			Description("Greet with permission").
			Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
				pm := bus.PermissionManagerFromContext(ctx)
				if err := pm.Ask(ctx, bus.PermissionRequest{
					Permission: "greet",
					Patterns:   []string{"*"},
					ToolCallID: opts.ToolCallID,
				}); err != nil {
					return tool.Result{}, err
				}
				return tool.Result{Output: "Hello!"}, nil
			}).Build()
		toolSet := tool.Set{"safe_echo": safeEcho, "perm_greet": permGreet}

		multiToolCall := func() []stream.Event {
			echoInput, _ := json.Marshal(map[string]string{})
			greetInput, _ := json.Marshal(map[string]string{})
			return []stream.Event{
				{Type: stream.EventToolCall, Data: stream.ToolCallEvent{
					ToolCallID: "call_echo", ToolName: "safe_echo", Input: echoInput,
				}},
				{Type: stream.EventToolCall, Data: stream.ToolCallEvent{
					ToolCallID: "call_greet", ToolName: "perm_greet", Input: greetInput,
				}},
				{Type: stream.EventFinish, Data: stream.FinishEvent{
					FinishReason: stream.FinishReasonToolCalls,
					Usage:        testutil.MockUsage(10, 5),
				}},
			}
		}

		// === Path A: auto-approve, both tools succeed ===
		mockA := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponses: [][]stream.Event{
				multiToolCall(),
				testutil.MockTextResponse("Both done!", testutil.MockUsage(15, 8)),
			},
		})
		runnerA := NewRunner(RunnerOptions{Agent: testAgent(toolSet), Model: mockA, Quiet: true})
		runnerA.PermissionManager().SetRules(allowAll)

		resultA, err := runnerA.Run(context.Background(), "echo and greet")
		if err != nil {
			t.Fatalf("Path A: unexpected error: %v", err)
		}
		if resultA.Status != RunCompleted {
			t.Fatalf("Path A: expected RunCompleted, got %s", resultA.Status)
		}

		// === Path B: safe_echo succeeds, perm_greet suspends ===

		mock2a := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponse: multiToolCall(),
		})
		runner2a := NewRunner(RunnerOptions{Agent: testAgent(toolSet), Model: mock2a, Quiet: true})

		result2a, err := runner2a.Run(context.Background(), "echo and greet")
		if err != nil {
			t.Fatalf("Path B phase 1: unexpected error: %v", err)
		}
		if result2a.Status != RunSuspended {
			t.Fatalf("Path B phase 1: expected RunSuspended, got %s", result2a.Status)
		}

		sc := result2a.SuspensionContext
		if sc == nil {
			t.Fatal("Path B phase 1: SuspensionContext should be non-nil")
		}
		if sc.Reason != "permission" {
			t.Errorf("expected reason 'permission', got %q", sc.Reason)
		}
		if len(sc.CompletedResults) != 1 {
			t.Errorf("expected 1 completed result (safe_echo), got %d", len(sc.CompletedResults))
		} else if sc.CompletedResults[0].ToolName != "safe_echo" {
			t.Errorf("expected completed tool 'safe_echo', got %q", sc.CompletedResults[0].ToolName)
		}
		if len(sc.PendingToolCalls) != 1 {
			t.Errorf("expected 1 pending tool call (perm_greet), got %d", len(sc.PendingToolCalls))
		} else if sc.PendingToolCalls[0].Name != "perm_greet" {
			t.Errorf("expected pending tool 'perm_greet', got %q", sc.PendingToolCalls[0].Name)
		}

		data, err := json.Marshal(result2a)
		if err != nil {
			t.Fatalf("Path B phase 2: marshal error: %v", err)
		}
		var checkpoint RunResult
		if err := json.Unmarshal(data, &checkpoint); err != nil {
			t.Fatalf("Path B phase 2: unmarshal error: %v", err)
		}

		mock2b := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
			StreamResponses: [][]stream.Event{
				testutil.MockToolCallResponse("call_greet_2", "perm_greet", map[string]string{}, testutil.MockUsage(10, 5)),
				testutil.MockTextResponse("Both done!", testutil.MockUsage(15, 8)),
			},
		})
		runner2b := NewRunner(RunnerOptions{
			Agent:           testAgent(toolSet),
			Model:           mock2b,
			InitialMessages: checkpoint.Messages,
			Quiet:           true,
		})
		runner2b.PermissionManager().SetRules(allowAll)

		resultB, err := runner2b.Run(context.Background(), "")
		if err != nil {
			t.Fatalf("Path B phase 3: unexpected error: %v", err)
		}
		if resultB.Status != RunCompleted {
			t.Fatalf("Path B phase 3: expected RunCompleted, got %s", resultB.Status)
		}

		// === Compare ===
		if resultA.TotalText != resultB.TotalText {
			t.Errorf("TotalText mismatch: Path A=%q, Path B=%q", resultA.TotalText, resultB.TotalText)
		}
		if resultA.TotalText != "Both done!" {
			t.Errorf("expected TotalText 'Both done!', got %q", resultA.TotalText)
		}
	})
}

func TestNewRunner_ScopedBusIsolation(t *testing.T) {
	a := testAgent(tool.Set{})

	runner1 := NewRunner(RunnerOptions{Agent: a, Quiet: true})
	runner2 := NewRunner(RunnerOptions{Agent: a, Quiet: true})

	if runner1.Bus() == runner2.Bus() {
		t.Error("two runners should have different buses by default")
	}

	if runner1.PermissionManager() == runner2.PermissionManager() {
		t.Error("two runners should have different permission managers")
	}

	var bus1Events, bus2Events int
	runner1.Bus().SubscribeAll(func(e bus.Event) { bus1Events++ })
	runner2.Bus().SubscribeAll(func(e bus.Event) { bus2Events++ })

	runner1.Bus().Publish("test.event", nil)

	if bus1Events != 1 {
		t.Errorf("bus1 should have seen 1 event, got %d", bus1Events)
	}
	if bus2Events != 0 {
		t.Errorf("bus2 should have seen 0 events, got %d", bus2Events)
	}
}

func TestFilterMessageParts(t *testing.T) {
	// Build a rich assistant message with reasoning, text, tool call, and image.
	richMsg := goai.NewAssistantMessageWithParts(
		goai.ReasoningPart{Text: "thinking..."},
		goai.TextPart{Text: "hello"},
		goai.ToolCallPart{ID: "tc1", Name: "bash", Input: json.RawMessage(`{}`)},
		message.ImagePart{Image: "base64data", MimeType: "image/png"},
	)

	toolMsg := goai.NewToolMessage("tc1", "bash", "output", false)

	tests := []struct {
		name           string
		msg            goai.Message
		policy         agent.HistoryPolicy
		wantPartCount  int // -1 means text-only (no parts)
		wantTextOnly   bool
		wantSkipAsTool bool // for tool messages with ExcludeToolCalls
	}{
		{
			name:          "no filtering",
			msg:           richMsg,
			policy:        agent.HistoryPolicy{},
			wantPartCount: 4,
		},
		{
			name:          "exclude reasoning",
			msg:           richMsg,
			policy:        agent.HistoryPolicy{ExcludeReasoning: true},
			wantPartCount: 3,
		},
		{
			name:          "exclude tool calls",
			msg:           richMsg,
			policy:        agent.HistoryPolicy{ExcludeToolCalls: true},
			wantPartCount: 3,
		},
		{
			name:          "exclude files",
			msg:           richMsg,
			policy:        agent.HistoryPolicy{ExcludeFiles: true},
			wantPartCount: 3, // image silently removed
		},
		{
			name:          "exclude reasoning + tools + files leaves only text",
			msg:           richMsg,
			policy:        agent.HistoryPolicy{ExcludeReasoning: true, ExcludeToolCalls: true, ExcludeFiles: true},
			wantPartCount: -1,
			wantTextOnly:  true,
		},
		{
			name:          "text-only message unchanged",
			msg:           goai.NewAssistantMessage("just text"),
			policy:        agent.HistoryPolicy{ExcludeReasoning: true, ExcludeToolCalls: true, ExcludeFiles: true},
			wantPartCount: -1,
			wantTextOnly:  true,
		},
		{
			name:          "tool result message unchanged with no filtering",
			msg:           toolMsg,
			policy:        agent.HistoryPolicy{},
			wantPartCount: 1, // ToolResultPart
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterMessageParts(tt.msg, tt.policy)

			if tt.wantTextOnly {
				if result.Content.IsMultiPart() {
					t.Errorf("expected text-only, got %d parts", len(result.Content.Parts))
				}
				return
			}

			if !result.Content.IsMultiPart() {
				t.Fatalf("expected multi-part, got text-only: %q", result.Content.Text)
			}
			if len(result.Content.Parts) != tt.wantPartCount {
				t.Errorf("got %d parts, want %d", len(result.Content.Parts), tt.wantPartCount)
			}
		})
	}
}

// TestRunner_Compact verifies user-triggered Compact:
// 1. Loads history from the store.
// 2. Asks the model to summarize (the one LLM call in this path).
// 3. Persists the compacted state via store.Compact with a non-negative
//    tokensFreed value — proving the shared compactNow helper wired both
//    paths correctly.
func TestRunner_Compact(t *testing.T) {
	store := &testStore{
		messages: []session.Message{
			{Role: "user", Content: "First question about a long topic"},
			{Role: "assistant", Content: "Long detailed answer " + strings.Repeat("a", 500)},
			{Role: "user", Content: "Follow-up question " + strings.Repeat("b", 400)},
			{Role: "assistant", Content: "Another detailed answer " + strings.Repeat("c", 600)},
		},
	}

	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("Conversation summary: user asked about topic X.", testutil.MockUsage(800, 40)),
	})

	runner := NewRunner(RunnerOptions{
		Agent:        testAgent(tool.Set{}),
		Model:        mockModel,
		SessionStore: store,
		Quiet:        true,
	})

	result, err := runner.Compact(context.Background())
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}

	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if !strings.Contains(result.Summary, "Conversation summary") {
		t.Errorf("summary = %q, want it to contain the mocked summary text", result.Summary)
	}
	if result.TokensFreed <= 0 {
		t.Errorf("TokensFreed = %d, want > 0 (history was substantially longer than summary)", result.TokensFreed)
	}
	if store.tokensFreed != result.TokensFreed {
		t.Errorf("store.tokensFreed = %d, want %d (store.Compact should receive same value)", store.tokensFreed, result.TokensFreed)
	}

	// After Compact, the store's messages are the new post-checkpoint set —
	// original user message + summary + synthetic continue, per CompactAndContinue.
	if len(store.messages) == 0 {
		t.Fatal("store should have messages after Compact")
	}
	foundSummary := false
	for _, m := range store.messages {
		if m.Summary {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Error("store messages should include the summary message flagged Summary=true")
	}
}

// TestRunner_Compact_NoStore verifies Compact rejects when no SessionStore
// is configured — there's nothing to persist to, so the call is incoherent.
func TestRunner_Compact_NoStore(t *testing.T) {
	mockModel := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("unused", testutil.MockUsage(1, 1)),
	})
	runner := NewRunner(RunnerOptions{
		Agent: testAgent(tool.Set{}),
		Model: mockModel,
		Quiet: true,
	})
	if _, err := runner.Compact(context.Background()); err == nil {
		t.Fatal("expected error when store is nil")
	}
}

// TestRunner_SuspensionWithStore verifies that on suspension:
// 1. The store contains the assistant tool-call message (so the caller can append results)
// 2. PendingToolCalls captures the incomplete call
// 3. After appending a tool result to the store, a new runner can resume cleanly.
func TestRunner_SuspensionWithStore(t *testing.T) {
	permTool := tool.New("perm_tool").
		Description("Tool that asks permission").
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			pm := bus.PermissionManagerFromContext(ctx)
			err := pm.Ask(ctx, bus.PermissionRequest{
				Permission: "dangerous",
				Patterns:   []string{"*"},
				ToolCallID: opts.ToolCallID,
			})
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Output: "executed"}, nil
		}).Build()

	store := &testStore{}

	// Step 1: Run with store — should suspend.
	mockModel1 := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockToolCallResponse("call_1", "perm_tool", map[string]string{"arg": "val"}, testutil.MockUsage(10, 5)),
	})

	runner1 := NewRunner(RunnerOptions{
		Agent:        testAgent(tool.Set{"perm_tool": permTool}),
		Model:        mockModel1,
		SessionStore: store,
		Quiet:        true,
	})

	result1, err := runner1.Run(context.Background(), "do it")
	if err != nil {
		t.Fatalf("suspension should not return error: %v", err)
	}
	if result1.Status != RunSuspended {
		t.Fatalf("expected RunSuspended, got %s", result1.Status)
	}

	// Verify pending tool calls captured.
	if len(result1.SuspensionContext.PendingToolCalls) != 1 {
		t.Fatalf("expected 1 pending tool call, got %d", len(result1.SuspensionContext.PendingToolCalls))
	}
	pending := result1.SuspensionContext.PendingToolCalls[0]
	if pending.ID != "call_1" || pending.Name != "perm_tool" {
		t.Errorf("pending tool call = %+v", pending)
	}

	// Verify store has the assistant tool-call message.
	hasToolCall := false
	for _, msg := range store.messages {
		if msg.Role == "assistant" {
			for _, p := range msg.Parts {
				if p.Type == "tool" && p.Tool != nil && p.Tool.CallID == "call_1" {
					hasToolCall = true
				}
			}
		}
	}
	if !hasToolCall {
		t.Fatal("store should contain assistant tool-call message after suspension")
	}

	// Step 2: Simulate agentsdk — append tool result to store.
	store.Append(context.Background(), []session.Message{{
		Role: "tool",
		Parts: []session.Part{{
			Type: "tool",
			Tool: &session.ToolPart{
				CallID: "call_1",
				Name:   "perm_tool",
				Output: "executed",
				Status: "completed",
			},
		}},
	}})

	// Step 3: Resume with a new runner using the same store.
	mockModel2 := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("all done!", testutil.MockUsage(15, 10)),
	})

	runner2 := NewRunner(RunnerOptions{
		Agent:        testAgent(tool.Set{"perm_tool": permTool}),
		Model:        mockModel2,
		SessionStore: store,
		Quiet:        true,
	})

	result2, err := runner2.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if result2.Status != RunCompleted {
		t.Errorf("expected RunCompleted, got %s", result2.Status)
	}
	if result2.TotalText != "all done!" {
		t.Errorf("expected 'all done!', got %q", result2.TotalText)
	}
}
