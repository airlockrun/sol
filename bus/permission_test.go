package bus

import (
	"context"
	"errors"
	"testing"
)

func TestPermissionManager_RulesAllow(t *testing.T) {
	b := New()
	pm := NewPermissionManager(b)

	pm.SetRules([]PermissionRule{
		{Permission: "read", Pattern: "*", Action: "allow"},
	})

	ctx := context.Background()
	err := pm.Ask(ctx, PermissionRequest{
		SessionID:  "test",
		Permission: "read",
		Patterns:   []string{"anything.txt"},
	})

	if err != nil {
		t.Errorf("expected allow rule to pass, got: %v", err)
	}
}

func TestPermissionManager_RulesDeny(t *testing.T) {
	b := New()
	pm := NewPermissionManager(b)

	pm.SetRules([]PermissionRule{
		{Permission: "bash", Pattern: "*", Action: "deny"},
	})

	ctx := context.Background()
	err := pm.Ask(ctx, PermissionRequest{
		SessionID:  "test",
		Permission: "bash",
		Patterns:   []string{"rm -rf /"},
	})

	if err == nil {
		t.Error("expected deny rule to fail")
	}

	var denied *PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Errorf("expected PermissionDeniedError, got %T", err)
	}
}

func TestPermissionManager_NoRuleReturnsErrPermissionNeeded(t *testing.T) {
	b := New()
	pm := NewPermissionManager(b)
	// No rules — default action is "ask"

	ctx := context.Background()
	err := pm.Ask(ctx, PermissionRequest{
		SessionID:  "test",
		Permission: "edit",
		Patterns:   []string{"test.txt"},
		ToolCallID: "call_1",
	})

	if err == nil {
		t.Fatal("expected ErrPermissionNeeded")
	}
	var permErr *ErrPermissionNeeded
	if !errors.As(err, &permErr) {
		t.Fatalf("expected ErrPermissionNeeded, got %T: %v", err, err)
	}
	if permErr.Permission != "edit" {
		t.Errorf("expected permission 'edit', got %q", permErr.Permission)
	}
	if permErr.ToolCallID != "call_1" {
		t.Errorf("expected toolCallID 'call_1', got %q", permErr.ToolCallID)
	}
	if len(permErr.Patterns) != 1 || permErr.Patterns[0] != "test.txt" {
		t.Errorf("expected patterns [test.txt], got %v", permErr.Patterns)
	}
}

func TestPermissionManager_AlwaysPublishesEvent(t *testing.T) {
	b := New()
	pm := NewPermissionManager(b)
	pm.SetRules([]PermissionRule{
		{Permission: "*", Pattern: "*", Action: "allow"},
	})

	var eventCount int
	b.Subscribe(PermissionAsked, func(e Event) {
		eventCount++
	})

	ctx := context.Background()
	pm.Ask(ctx, PermissionRequest{
		SessionID:  "test",
		Permission: "read",
		Patterns:   []string{"file.txt"},
	})

	if eventCount != 1 {
		t.Errorf("expected 1 event published, got %d", eventCount)
	}
}

func TestPermissionManager_AddRule(t *testing.T) {
	b := New()
	pm := NewPermissionManager(b)

	ctx := context.Background()

	// No rules — returns ErrPermissionNeeded
	err := pm.Ask(ctx, PermissionRequest{
		SessionID:  "test",
		Permission: "edit",
		Patterns:   []string{"test.txt"},
	})
	var permErr *ErrPermissionNeeded
	if !errors.As(err, &permErr) {
		t.Fatalf("expected ErrPermissionNeeded before AddRule, got %v", err)
	}

	// Add allow rule dynamically
	pm.AddRule(PermissionRule{Permission: "edit", Pattern: "*.txt", Action: "allow"})

	// Now should pass
	err = pm.Ask(ctx, PermissionRequest{
		SessionID:  "test",
		Permission: "edit",
		Patterns:   []string{"test.txt"},
	})
	if err != nil {
		t.Errorf("expected allow after AddRule, got %v", err)
	}
}

func TestPermissionManager_ErrPermissionNeeded_IsFatalToolError(t *testing.T) {
	err := &ErrPermissionNeeded{Permission: "edit"}
	if !err.FatalToolError() {
		t.Error("expected FatalToolError() to return true")
	}
}

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		value   string
		pattern string
		want    bool
	}{
		{"anything", "*", true},
		{"test.txt", "*.txt", true},
		{"test.go", "*.txt", false},
		{"prefix_test", "prefix*", true},
		{"test_suffix", "*suffix", true},
		{"exact", "exact", true},
		{"different", "exact", false},
		{"", "*", true},
		{"test", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.value+"_"+tt.pattern, func(t *testing.T) {
			got := matchWildcard(tt.value, tt.pattern)
			if got != tt.want {
				t.Errorf("matchWildcard(%q, %q) = %v, want %v", tt.value, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestQuestionManager_AutoAnswer(t *testing.T) {
	b := New()
	qm := NewQuestionManager(b)
	qm.SetAutoAnswer(true)

	ctx := context.Background()
	answers, err := qm.Ask(ctx, AskInput{
		SessionID: "test",
		Questions: []QuestionInfo{
			{Question: "test?", Header: "Test", Options: []QuestionOption{{Label: "A"}}},
			{Question: "test2?", Header: "Test2", Options: []QuestionOption{{Label: "B"}}},
		},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(answers) != 2 {
		t.Fatalf("expected 2 answer sets, got %d", len(answers))
	}
	for i, a := range answers {
		if len(a) != 0 {
			t.Errorf("expected empty answer for question %d, got: %v", i, a)
		}
	}
}

func TestQuestionManager_PreloadedAnswers(t *testing.T) {
	b := New()
	qm := NewQuestionManager(b)

	// Push pre-loaded answers
	qm.PushAnswers([][]string{{"Red"}})
	qm.PushAnswers([][]string{{"Blue"}})

	ctx := context.Background()

	// First ask should get "Red"
	answers, err := qm.Ask(ctx, AskInput{
		SessionID: "test",
		Questions: []QuestionInfo{{Question: "color?", Header: "Color"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "Red" {
		t.Errorf("expected [[Red]], got %v", answers)
	}

	// Second ask should get "Blue"
	answers, err = qm.Ask(ctx, AskInput{
		SessionID: "test",
		Questions: []QuestionInfo{{Question: "color2?", Header: "Color2"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "Blue" {
		t.Errorf("expected [[Blue]], got %v", answers)
	}
}

func TestQuestionManager_NoAnswerReturnsErrQuestionNeeded(t *testing.T) {
	b := New()
	qm := NewQuestionManager(b)
	// No answers, no auto-answer

	ctx := context.Background()
	_, err := qm.Ask(ctx, AskInput{
		SessionID: "test",
		Questions: []QuestionInfo{
			{Question: "test?", Header: "Test", Options: []QuestionOption{{Label: "A"}}},
		},
	})

	if err == nil {
		t.Fatal("expected ErrQuestionNeeded")
	}
	var questErr *ErrQuestionNeeded
	if !errors.As(err, &questErr) {
		t.Fatalf("expected ErrQuestionNeeded, got %T: %v", err, err)
	}
	if len(questErr.Questions) != 1 {
		t.Errorf("expected 1 question in error, got %d", len(questErr.Questions))
	}
}

func TestQuestionManager_AlwaysPublishesEvent(t *testing.T) {
	b := New()
	qm := NewQuestionManager(b)
	qm.SetAutoAnswer(true)

	var eventCount int
	b.Subscribe(QuestionAsked, func(e Event) {
		eventCount++
	})

	ctx := context.Background()
	qm.Ask(ctx, AskInput{
		SessionID: "test",
		Questions: []QuestionInfo{{Question: "test?", Header: "Test"}},
	})

	if eventCount != 1 {
		t.Errorf("expected 1 event, got %d", eventCount)
	}
}

func TestQuestionManager_ScriptedAnswers(t *testing.T) {
	b := New()
	qm := NewQuestionManager(b)

	// Push scripted answers (numeric indices, resolved at ask time)
	qm.PushScriptedAnswers([]string{"2", "custom text"})

	ctx := context.Background()

	// First ask: "2" should resolve to second option label
	answers, err := qm.Ask(ctx, AskInput{
		SessionID: "test",
		Questions: []QuestionInfo{
			{
				Question: "Pick one",
				Header:   "Choice",
				Options: []QuestionOption{
					{Label: "Alpha"},
					{Label: "Beta"},
					{Label: "Gamma"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "Beta" {
		t.Errorf("expected [[Beta]], got %v", answers)
	}

	// Second ask: "custom text" should be used as-is
	answers, err = qm.Ask(ctx, AskInput{
		SessionID: "test",
		Questions: []QuestionInfo{
			{
				Question: "What else?",
				Header:   "Other",
				Options:  []QuestionOption{{Label: "A"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "custom text" {
		t.Errorf("expected [[custom text]], got %v", answers)
	}
}

func TestQuestionManager_ErrQuestionNeeded_IsFatalToolError(t *testing.T) {
	err := &ErrQuestionNeeded{Questions: []QuestionInfo{{Question: "test?"}}}
	if !err.FatalToolError() {
		t.Error("expected FatalToolError() to return true")
	}
}
