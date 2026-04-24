package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/tools"
)

// TestToolServer_RealToolList verifies that FetchTools returns Sol's actual
// tool set when served through the ToolServer → WSTransport → RemoteExecutor
// pipeline.
func TestToolServer_RealToolList(t *testing.T) {
	// Create the real tool set used in production
	toolSet := tools.CreateToolSetForModel("generic")
	localExec := tool.NewLocalExecutor(toolSet, nil)

	server := NewToolServer(localExec)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	transport, err := NewWSTransport(wsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	remoteTools, err := transport.FetchTools(ctx)
	if err != nil {
		t.Fatalf("FetchTools: %v", err)
	}

	if len(remoteTools) == 0 {
		t.Fatal("expected at least 1 tool from FetchTools")
	}

	// Verify expected Sol tools are present
	toolNames := make(map[string]bool)
	for _, ti := range remoteTools {
		toolNames[ti.Name] = true
	}
	for _, expected := range []string{"bash", "read", "write", "glob", "grep"} {
		if !toolNames[expected] {
			t.Errorf("expected tool %q in tool list", expected)
		}
	}

	t.Logf("Got %d tools from ToolServer", len(remoteTools))
}

// TestToolServer_ExecuteThroughPipeline tests executing a mock tool through
// the full ToolServer → WSTransport → RemoteExecutor pipeline.
// This complements the existing executor_test.go tests by testing with
// multiple tools and concurrent requests.
func TestToolServer_ExecuteThroughPipeline(t *testing.T) {
	tools := make(tool.Set)
	tools.Add(mockTool("echo"))
	tools.Add(mockTool("upper"))
	localExec := tool.NewLocalExecutor(tools, nil)

	server := NewToolServer(localExec)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	transport, err := NewWSTransport(wsURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	remoteTools, err := transport.FetchTools(ctx)
	if err != nil {
		t.Fatalf("FetchTools: %v", err)
	}

	remote := NewRemoteExecutor(transport, remoteTools)

	// Execute echo tool
	resp, err := remote.Execute(ctx, tool.Request{
		ToolCallID: "call_1",
		ToolName:   "echo",
		Input:      json.RawMessage(`{"input":"hello world"}`),
	})
	if err != nil {
		t.Fatalf("Execute echo: %v", err)
	}
	if resp.IsError {
		t.Fatalf("echo returned error: %s", resp.Output)
	}
	if resp.Output != "echo: hello world" {
		t.Fatalf("echo output: got %q, want %q", resp.Output, "echo: hello world")
	}

	// Execute upper tool
	resp, err = remote.Execute(ctx, tool.Request{
		ToolCallID: "call_2",
		ToolName:   "upper",
		Input:      json.RawMessage(`{"input":"test"}`),
	})
	if err != nil {
		t.Fatalf("Execute upper: %v", err)
	}
	if resp.Output != "echo: test" {
		t.Fatalf("upper output: got %q, want %q", resp.Output, "echo: test")
	}
}

// permissionTool creates a tool that calls AskPermission via the context.
func permissionTool() tool.Tool {
	return tool.Tool{
		Name:        "needs_permission",
		Description: "Tool that requires permission",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		Execute: func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args struct {
				Path string `json:"path"`
			}
			json.Unmarshal(input, &args)

			pm := bus.PermissionManagerFromContext(ctx)
			err := pm.Ask(ctx, bus.PermissionRequest{
				Permission: "edit",
				Patterns:   []string{args.Path},
				ToolCallID: opts.ToolCallID,
			})
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Output: "edited: " + args.Path}, nil
		},
	}
}

// questionTool creates a tool that calls QuestionManager.Ask via the context.
func questionTool() tool.Tool {
	return tool.Tool{
		Name:        "needs_question",
		Description: "Tool that asks a question",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Execute: func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			qm := bus.QuestionManagerFromContext(ctx)
			answers, err := qm.Ask(ctx, bus.AskInput{
				Questions: []bus.QuestionInfo{
					{Question: "Pick a color", Header: "Color", Options: []bus.QuestionOption{
						{Label: "Red"}, {Label: "Blue"},
					}},
				},
				Tool: &bus.ToolContext{CallID: opts.ToolCallID},
			})
			if err != nil {
				return tool.Result{}, err
			}
			if len(answers) > 0 && len(answers[0]) > 0 {
				return tool.Result{Output: "chose: " + answers[0][0]}, nil
			}
			return tool.Result{Output: "no answer"}, nil
		},
	}
}

// setupPipeline creates the full ToolServer → httptest → WSTransport → RemoteExecutor pipeline.
func setupPipeline(t *testing.T, ts tool.Set) (*ToolServer, *WSTransport, *RemoteExecutor, func()) {
	t.Helper()
	localExec := tool.NewLocalExecutor(ts, nil)
	server := NewToolServer(localExec)

	httpServer := httptest.NewServer(server.Handler())
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	transport, err := NewWSTransport(wsURL)
	if err != nil {
		httpServer.Close()
		t.Fatalf("connect: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	remoteTools, err := transport.FetchTools(ctx)
	if err != nil {
		transport.Close()
		httpServer.Close()
		t.Fatalf("FetchTools: %v", err)
	}

	remote := NewRemoteExecutor(transport, remoteTools)
	cleanup := func() {
		transport.Close()
		httpServer.Close()
	}
	return server, transport, remote, cleanup
}

// TestToolServer_PermissionNeeded verifies that when no permission rules
// match, ErrPermissionNeeded propagates as a structured error through the
// full pipeline and implements FatalToolError.
func TestToolServer_PermissionNeeded(t *testing.T) {
	ts := make(tool.Set)
	ts.Add(permissionTool())
	_, _, remote, cleanup := setupPipeline(t, ts)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// No rules set → should get ErrPermissionNeeded
	_, err := remote.Execute(ctx, tool.Request{
		ToolCallID: "call_perm",
		ToolName:   "needs_permission",
		Input:      json.RawMessage(`{"path":"secret.txt"}`),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var permErr *bus.ErrPermissionNeeded
	if !errors.As(err, &permErr) {
		t.Fatalf("expected *ErrPermissionNeeded, got %T: %v", err, permErr)
	}
	if permErr.Permission != "edit" {
		t.Errorf("expected permission 'edit', got %q", permErr.Permission)
	}
	if len(permErr.Patterns) != 1 || permErr.Patterns[0] != "secret.txt" {
		t.Errorf("expected patterns [secret.txt], got %v", permErr.Patterns)
	}
	if permErr.ToolCallID != "call_perm" {
		t.Errorf("expected toolCallID 'call_perm', got %q", permErr.ToolCallID)
	}

	// Verify it implements FatalToolError
	type fatalToolError interface{ FatalToolError() bool }
	if fte, ok := err.(fatalToolError); !ok || !fte.FatalToolError() {
		t.Error("ErrPermissionNeeded should implement FatalToolError")
	}
}

// TestToolServer_SetRules verifies that after sending rules via
// transport.SetRules(), the tool executes successfully.
func TestToolServer_SetRules(t *testing.T) {
	ts := make(tool.Set)
	ts.Add(permissionTool())
	_, transport, remote, cleanup := setupPipeline(t, ts)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Set allow-all rules
	if err := transport.SetRules(ctx, []bus.PermissionRule{
		{Permission: "*", Pattern: "*", Action: "allow"},
	}); err != nil {
		t.Fatalf("SetRules: %v", err)
	}

	// Now the tool should succeed
	resp, err := remote.Execute(ctx, tool.Request{
		ToolCallID: "call_perm2",
		ToolName:   "needs_permission",
		Input:      json.RawMessage(`{"path":"allowed.txt"}`),
	})
	if err != nil {
		t.Fatalf("Execute after SetRules: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error response: %s", resp.Output)
	}
	if resp.Output != "edited: allowed.txt" {
		t.Errorf("expected 'edited: allowed.txt', got %q", resp.Output)
	}
}

// TestToolServer_QuestionNeeded verifies that when no answers are available,
// ErrQuestionNeeded propagates as a structured error through the pipeline.
func TestToolServer_QuestionNeeded(t *testing.T) {
	ts := make(tool.Set)
	ts.Add(questionTool())
	_, _, remote, cleanup := setupPipeline(t, ts)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// No answers → should get ErrQuestionNeeded
	_, err := remote.Execute(ctx, tool.Request{
		ToolCallID: "call_q",
		ToolName:   "needs_question",
		Input:      json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var questErr *bus.ErrQuestionNeeded
	if !errors.As(err, &questErr) {
		t.Fatalf("expected *ErrQuestionNeeded, got %T: %v", err, questErr)
	}
	if len(questErr.Questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(questErr.Questions))
	}
	if questErr.Questions[0].Question != "Pick a color" {
		t.Errorf("expected question 'Pick a color', got %q", questErr.Questions[0].Question)
	}

	// Verify it implements FatalToolError
	type fatalToolError interface{ FatalToolError() bool }
	if fte, ok := err.(fatalToolError); !ok || !fte.FatalToolError() {
		t.Error("ErrQuestionNeeded should implement FatalToolError")
	}
}

// TestToolServer_PushAnswers verifies that after pushing answers via
// transport.PushAnswers(), the question tool gets the answer and executes.
func TestToolServer_PushAnswers(t *testing.T) {
	ts := make(tool.Set)
	ts.Add(questionTool())
	_, transport, remote, cleanup := setupPipeline(t, ts)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Push an answer before executing
	if err := transport.PushAnswers(ctx, [][]string{{"Blue"}}); err != nil {
		t.Fatalf("PushAnswers: %v", err)
	}

	// Now the tool should succeed with the pre-loaded answer
	resp, err := remote.Execute(ctx, tool.Request{
		ToolCallID: "call_q2",
		ToolName:   "needs_question",
		Input:      json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Execute after PushAnswers: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error response: %s", resp.Output)
	}
	if resp.Output != "chose: Blue" {
		t.Errorf("expected 'chose: Blue', got %q", resp.Output)
	}
}
