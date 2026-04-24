package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
)

// QuestionOption represents a single choice option
type QuestionOption struct {
	Label       string `json:"label" description:"Display text (1-5 words, concise)"`
	Description string `json:"description" description:"Explanation of choice"`
}

// QuestionItem represents a single question with options
type QuestionItem struct {
	Question string           `json:"question" description:"Complete question"`
	Header   string           `json:"header" description:"Very short label (max 30 chars)"`
	Options  []QuestionOption `json:"options" description:"Available choices" itemRef:"QuestionOption"`
	Multiple bool             `json:"multiple,omitempty" description:"Allow selecting multiple choices"`
}

// QuestionInput is the input schema for the question tool
type QuestionInput struct {
	Questions []QuestionItem `json:"questions" description:"Questions to ask"`
}

// Question creates the question tool
func Question() tool.Tool {
	return tool.New("question").
		Description(`Use this tool when you need to ask the user questions during execution. This allows you to:
1. Gather user preferences or requirements
2. Clarify ambiguous instructions
3. Get decisions on implementation choices as you work
4. Offer choices to the user about what direction to take.

Usage notes:
- When ` + "`custom`" + ` is enabled (default), a "Type your own answer" option is added automatically; don't include "Other" or catch-all options
- Answers are returned as arrays of labels; set ` + "`multiple: true`" + ` to allow selecting more than one
- If you recommend a specific option, make that the first option in the list and add "(Recommended)" at the end of the label
`).
		SchemaFromStruct(QuestionInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args QuestionInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			if len(args.Questions) == 0 {
				return tool.Result{Output: "[No questions provided]", Title: "Question"}, nil
			}

			// Convert to bus.QuestionInfo
			busQuestions := make([]bus.QuestionInfo, len(args.Questions))
			for i, q := range args.Questions {
				opts := make([]bus.QuestionOption, len(q.Options))
				for j, opt := range q.Options {
					opts[j] = bus.QuestionOption{
						Label:       opt.Label,
						Description: opt.Description,
					}
				}
				busQuestions[i] = bus.QuestionInfo{
					Question: q.Question,
					Header:   q.Header,
					Options:  opts,
					Multiple: q.Multiple,
					Custom:   true, // Default to allowing custom answers
				}
			}

			// Ask questions through context-scoped QuestionManager
			qm := bus.QuestionManagerFromContext(ctx)
			answers, err := qm.Ask(ctx, bus.AskInput{
				SessionID: opts.ToolCallID,
				Questions: busQuestions,
				Tool:      &bus.ToolContext{CallID: opts.ToolCallID},
			})
			if err != nil {
				return tool.Result{}, err // ErrQuestionNeeded (FatalToolError)
			}

			// Format output like opencode
			formatted := make([]string, len(args.Questions))
			for i, q := range args.Questions {
				answer := "Unanswered"
				if i < len(answers) && len(answers[i]) > 0 {
					answer = strings.Join(answers[i], ", ")
				}
				formatted[i] = fmt.Sprintf(`"%s"="%s"`, q.Question, answer)
			}

			// Build title
			title := fmt.Sprintf("Asked %d question", len(args.Questions))
			if len(args.Questions) > 1 {
				title += "s"
			}

			return tool.Result{
				Output: fmt.Sprintf("User has answered your questions: %s. You can now continue with the user's answers in mind.",
					strings.Join(formatted, ", ")),
				Title: title,
				Metadata: map[string]any{
					"answers": answers,
				},
			}, nil
		}).
		Build()
}
