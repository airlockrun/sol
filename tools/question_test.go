package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/toolutil"
)

// setupQuestionContext creates a context with a QuestionManager pre-loaded with answers.
func setupQuestionContext(answers ...[][]string) context.Context {
	b := bus.New()
	qm := bus.NewQuestionManager(b)
	for _, a := range answers {
		qm.PushAnswers(a)
	}
	ctx := bus.WithQuestionManager(context.Background(), qm)
	ctx = bus.WithBus(ctx, b)
	return ctx
}

func TestQuestionTool_SingleQuestion(t *testing.T) {
	ctx := setupQuestionContext([][]string{{"Red"}})

	q := Question()
	input, _ := json.Marshal(QuestionInput{
		Questions: []QuestionItem{
			{
				Question: "What is your favorite color?",
				Header:   "Color",
				Options: []QuestionOption{
					{Label: "Red", Description: "The color of passion"},
					{Label: "Blue", Description: "The color of sky"},
				},
			},
		},
	})

	result, err := q.Execute(ctx, input, tool.CallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Title != "Asked 1 question" {
		t.Errorf("expected title 'Asked 1 question', got '%s'", result.Title)
	}

	if !strings.Contains(result.Output, `"What is your favorite color?"="Red"`) {
		t.Errorf("expected output to contain question/answer, got: %s", result.Output)
	}

	if !strings.Contains(result.Output, "User has answered your questions") {
		t.Errorf("expected output format, got: %s", result.Output)
	}
}

func TestQuestionTool_MultipleQuestions(t *testing.T) {
	ctx := setupQuestionContext([][]string{{"Red"}, {"Dog"}})

	q := Question()
	input, _ := json.Marshal(QuestionInput{
		Questions: []QuestionItem{
			{
				Question: "What is your favorite color?",
				Header:   "Color",
				Options:  []QuestionOption{{Label: "Red", Description: ""}},
			},
			{
				Question: "What is your favorite animal?",
				Header:   "Animal",
				Options:  []QuestionOption{{Label: "Dog", Description: ""}},
			},
		},
	})

	result, err := q.Execute(ctx, input, tool.CallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Title != "Asked 2 questions" {
		t.Errorf("expected title 'Asked 2 questions', got '%s'", result.Title)
	}

	if !strings.Contains(result.Output, `"What is your favorite color?"="Red"`) {
		t.Errorf("expected first question/answer, got: %s", result.Output)
	}

	if !strings.Contains(result.Output, `"What is your favorite animal?"="Dog"`) {
		t.Errorf("expected second question/answer, got: %s", result.Output)
	}
}

func TestQuestionTool_UnansweredQuestion(t *testing.T) {
	ctx := setupQuestionContext([][]string{{}})

	q := Question()
	input, _ := json.Marshal(QuestionInput{
		Questions: []QuestionItem{
			{
				Question: "What is your choice?",
				Header:   "Choice",
				Options:  []QuestionOption{{Label: "A", Description: ""}},
			},
		},
	})

	result, err := q.Execute(ctx, input, tool.CallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Output, `"What is your choice?"="Unanswered"`) {
		t.Errorf("expected 'Unanswered' for empty answer, got: %s", result.Output)
	}
}

func TestQuestionTool_AnswersInMetadata(t *testing.T) {
	ctx := setupQuestionContext([][]string{{"Option A", "Option B"}})

	q := Question()
	input, _ := json.Marshal(QuestionInput{
		Questions: []QuestionItem{
			{
				Question: "Select options",
				Header:   "Multi",
				Multiple: true,
				Options:  []QuestionOption{{Label: "Option A", Description: ""}, {Label: "Option B", Description: ""}},
			},
		},
	})

	result, err := q.Execute(ctx, input, tool.CallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	answers, ok := result.Metadata["answers"].([][]string)
	if !ok {
		t.Fatalf("expected answers in metadata, got: %v", result.Metadata)
	}

	if len(answers) != 1 || len(answers[0]) != 2 {
		t.Errorf("expected [[Option A, Option B]], got: %v", answers)
	}
}

func TestQuestionTool_NoAnswerSuspends(t *testing.T) {
	// QuestionManager with no answers and no auto-answer → ErrQuestionNeeded
	b := bus.New()
	qm := bus.NewQuestionManager(b)
	ctx := bus.WithQuestionManager(context.Background(), qm)
	ctx = bus.WithBus(ctx, b)

	q := Question()
	input, _ := json.Marshal(QuestionInput{
		Questions: []QuestionItem{
			{
				Question: "Test question?",
				Header:   "Test",
				Options:  []QuestionOption{{Label: "A", Description: ""}},
			},
		},
	})

	_, err := q.Execute(ctx, input, tool.CallOptions{})
	if err == nil {
		t.Fatal("expected ErrQuestionNeeded error")
	}
	var questErr *bus.ErrQuestionNeeded
	if !errors.As(err, &questErr) {
		t.Errorf("expected ErrQuestionNeeded, got %T: %v", err, err)
	}
}

func TestQuestionTool_NoQuestions(t *testing.T) {
	ctx := setupQuestionContext()

	q := Question()
	input, _ := json.Marshal(QuestionInput{
		Questions: []QuestionItem{},
	})

	result, err := q.Execute(ctx, input, tool.CallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Output != "[No questions provided]" {
		t.Errorf("expected '[No questions provided]', got: %s", result.Output)
	}
}

func TestStdioQuestionHandler_SingleSelection(t *testing.T) {
	input := bytes.NewBufferString("1\n")
	output := &bytes.Buffer{}

	handler := toolutil.NewStdioQuestionHandler(input, output)

	questions := []bus.QuestionInfo{
		{
			Question: "Pick a color",
			Header:   "Color",
			Options: []bus.QuestionOption{
				{Label: "Red", Description: "Primary"},
				{Label: "Blue", Description: "Cool"},
			},
		},
	}

	answers, err := handler.Ask(context.Background(), questions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "Red" {
		t.Errorf("expected [[Red]], got: %v", answers)
	}
}

func TestStdioQuestionHandler_CustomAnswer(t *testing.T) {
	input := bytes.NewBufferString("0\nMy custom answer\n")
	output := &bytes.Buffer{}

	handler := toolutil.NewStdioQuestionHandler(input, output)

	questions := []bus.QuestionInfo{
		{
			Question: "What do you want?",
			Header:   "Custom",
			Options: []bus.QuestionOption{
				{Label: "Option A", Description: ""},
			},
		},
	}

	answers, err := handler.Ask(context.Background(), questions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(answers) != 1 || len(answers[0]) != 1 || answers[0][0] != "My custom answer" {
		t.Errorf("expected [[My custom answer]], got: %v", answers)
	}
}

func TestStdioQuestionHandler_MultipleSelection(t *testing.T) {
	input := bytes.NewBufferString("1,2\n")
	output := &bytes.Buffer{}

	handler := toolutil.NewStdioQuestionHandler(input, output)

	questions := []bus.QuestionInfo{
		{
			Question: "Pick colors",
			Header:   "Colors",
			Multiple: true,
			Options: []bus.QuestionOption{
				{Label: "Red", Description: ""},
				{Label: "Blue", Description: ""},
				{Label: "Green", Description: ""},
			},
		},
	}

	answers, err := handler.Ask(context.Background(), questions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(answers) != 1 || len(answers[0]) != 2 {
		t.Errorf("expected 2 selections, got: %v", answers)
	}
	if answers[0][0] != "Red" || answers[0][1] != "Blue" {
		t.Errorf("expected [Red, Blue], got: %v", answers[0])
	}
}

func TestAutoAnswerHandler(t *testing.T) {
	handler := &toolutil.AutoAnswerHandler{}

	questions := []bus.QuestionInfo{
		{Question: "Q1", Header: "H1", Options: []bus.QuestionOption{{Label: "A", Description: ""}}},
		{Question: "Q2", Header: "H2", Options: []bus.QuestionOption{{Label: "B", Description: ""}}},
	}

	answers, err := handler.Ask(context.Background(), questions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(answers) != 2 {
		t.Errorf("expected 2 answer sets, got: %d", len(answers))
	}

	for i, a := range answers {
		if len(a) != 0 {
			t.Errorf("expected empty answer for question %d, got: %v", i, a)
		}
	}
}
