package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/agent"
	"github.com/airlockrun/sol/bus"
)

func TestCallQuestionAnswers(t *testing.T) {
	tools := tool.Set{}
	tools.Add(questionTool())
	_, _, remote, cleanup := setupPipeline(t, tools)
	defer cleanup()
	ctx := bus.WithCallQuestionAnswers(t.Context(), bus.CallQuestionAnswers{ToolCallID: "answer-this", Answers: [][]string{{"Blue"}}})
	request := tool.Request{ToolCallID: "answer-this", ToolName: "needs_question", Input: json.RawMessage(`{}`)}
	response, err := remote.Execute(ctx, request)
	if err != nil || response.Output != "chose: Blue" {
		t.Fatalf("answer = %+v %v", response, err)
	}
	request.ToolCallID = "unrelated"
	_, err = remote.Execute(ctx, request)
	var question *bus.ErrQuestionNeeded
	if !errors.As(err, &question) {
		t.Fatalf("answer leaked to unrelated call: %v", err)
	}
	request.ToolCallID = "answer-this"
	_, err = remote.Execute(t.Context(), request)
	if !errors.As(err, &question) {
		t.Fatalf("answer leaked into server queue: %v", err)
	}
}

func TestRemoteQuestionSuspensionResume(t *testing.T) {
	tools := tool.Set{}
	tools.Add(questionTool())
	_, _, remote, cleanup := setupPipeline(t, tools)
	defer cleanup()
	request := tool.Request{ToolCallID: "office", ToolName: "needs_question", Input: json.RawMessage(`{}`)}
	_, err := remote.Execute(t.Context(), request)
	var question *bus.ErrQuestionNeeded
	if !errors.As(err, &question) {
		t.Fatalf("missing typed question: %v", err)
	}
	suspension := &sol.SuspensionContext{Reason: "question", ToolCallID: request.ToolCallID, Data: question, PendingToolCalls: []stream.ToolCall{{ID: request.ToolCallID, Name: request.ToolName, Input: request.Input}}}
	raw, err := json.Marshal(suspension)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &suspension); err != nil {
		t.Fatal(err)
	}
	runner := sol.NewRunner(sol.RunnerOptions{Agent: &agent.Agent{Name: "remote", Tools: tools}, Executor: remote, Quiet: true})
	resolved, err := runner.ResolveQuestionSuspension(t.Context(), suspension, [][]string{{"Blue"}})
	if err != nil || resolved.SuspensionContext != nil || len(resolved.Results) != 1 {
		t.Fatalf("remote resume: %+v %v", resolved, err)
	}
	if output := resolved.Results[0].Output; !strings.Contains(fmt.Sprint(output), "chose: Blue") {
		t.Fatalf("answer absent: %+v", output)
	}
}
