package sol

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
)

func TestQuestionAnswersValidation(t *testing.T) {
	suspension := &SuspensionContext{Reason: "question", ToolCallID: "q", PendingToolCalls: []stream.ToolCall{{ID: "q", Name: "arbitrary"}}, Data: &bus.ErrQuestionNeeded{Questions: []bus.QuestionInfo{{Question: "Choose", Options: []bus.QuestionOption{{Label: "PDF"}, {Label: "Edit"}}}}}}
	for _, tt := range []struct {
		name    string
		answers [][]string
		valid   bool
	}{
		{"label", [][]string{{"PDF"}}, true}, {"missing", nil, false}, {"empty", [][]string{{}}, false}, {"unknown", [][]string{{"Allow"}}, false}, {"multiple", [][]string{{"PDF", "Edit"}}, false}, {"duplicate", [][]string{{"PDF", "PDF"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := suspension.ValidateQuestionAnswers(tt.answers); (err == nil) != tt.valid {
				t.Fatalf("validation = %v", err)
			}
		})
	}
	suspension.Data.(*bus.ErrQuestionNeeded).Questions[0].Custom = true
	if err := suspension.ValidateQuestionAnswers([][]string{{"My choice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(RunnerOptions{Agent: testAgent(nil), Quiet: true}).ResolvePermissionSuspension(t.Context(), suspension, true); err == nil {
		t.Fatal("question accepted as permission")
	}
}

func TestResolveQuestionKeepsFollowingGates(t *testing.T) {
	questions := []bus.QuestionInfo{{Question: "Choose", Options: []bus.QuestionOption{{Label: "PDF"}}}}
	var received [][]string
	tools := tool.Set{
		"not_named_question": tool.New("not_named_question").Execute(func(ctx context.Context, _ json.RawMessage, _ tool.CallOptions) (tool.Result, error) {
			var err error
			received, err = bus.QuestionManagerFromContext(ctx).Ask(ctx, bus.AskInput{Questions: questions})
			return tool.Result{Output: "answered"}, err
		}).Build(),
		"permission": tool.New("permission").Execute(func(ctx context.Context, _ json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			return tool.Result{}, bus.PermissionManagerFromContext(ctx).Ask(ctx, bus.PermissionRequest{Permission: "effect", Patterns: []string{"effect"}, ToolCallID: opts.ToolCallID})
		}).Build(),
	}
	suspension := &SuspensionContext{Reason: "question", ToolCallID: "q", PendingToolCalls: []stream.ToolCall{{ID: "q", Name: "not_named_question", Input: json.RawMessage(`{}`)}, {ID: "p", Name: "permission", Input: json.RawMessage(`{}`)}}, Data: &bus.ErrQuestionNeeded{Questions: questions}}
	// Durable JSON loses concrete interface types; the contract must survive it.
	raw, _ := json.Marshal(suspension)
	if err := json.Unmarshal(raw, &suspension); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(RunnerOptions{Agent: testAgent(tools), Quiet: true})
	resolved, err := runner.ResolveQuestionSuspension(t.Context(), suspension, [][]string{{"PDF"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received, [][]string{{"PDF"}}) || resolved.SuspensionContext == nil || resolved.SuspensionContext.Reason != "permission" || resolved.SuspensionContext.ToolCallID != "p" {
		t.Fatalf("lost answer or following gate: %+v %+v", received, resolved)
	}
}
