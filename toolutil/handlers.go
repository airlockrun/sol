package toolutil

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/airlockrun/sol/bus"
)

// QuestionHandler handles question requests.
type QuestionHandler interface {
	// Ask presents questions to the user and returns their answers
	Ask(ctx context.Context, questions []bus.QuestionInfo) ([][]string, error)
}

// AutoAnswerHandler automatically answers questions with empty responses.
type AutoAnswerHandler struct{}

func (h *AutoAnswerHandler) Ask(ctx context.Context, questions []bus.QuestionInfo) ([][]string, error) {
	// In auto mode, return empty answers for each question
	answers := make([][]string, len(questions))
	for i := range questions {
		answers[i] = []string{}
	}
	return answers, nil
}

// StdioQuestionHandler prompts the user via stdin/stdout.
type StdioQuestionHandler struct {
	Reader io.Reader
	Writer io.Writer
}

func (h *StdioQuestionHandler) Ask(ctx context.Context, questions []bus.QuestionInfo) ([][]string, error) {
	scanner := bufio.NewScanner(h.Reader)
	answers := make([][]string, len(questions))

	for i, q := range questions {
		// Display the question
		fmt.Fprintf(h.Writer, "\n=== Question: %s ===\n", q.Header)
		fmt.Fprintf(h.Writer, "%s\n\n", q.Question)

		// Display options
		for j, opt := range q.Options {
			fmt.Fprintf(h.Writer, "  [%d] %s\n", j+1, opt.Label)
			if opt.Description != "" {
				fmt.Fprintf(h.Writer, "      %s\n", opt.Description)
			}
		}
		fmt.Fprintf(h.Writer, "  [0] Type your own answer\n")

		if q.Multiple {
			fmt.Fprintf(h.Writer, "\nEnter numbers separated by commas (e.g., 1,2,3): ")
		} else {
			fmt.Fprintf(h.Writer, "\nEnter your choice: ")
		}

		if !scanner.Scan() {
			return nil, fmt.Errorf("failed to read input")
		}

		input := strings.TrimSpace(scanner.Text())
		var selectedAnswers []string

		if q.Multiple {
			// Parse comma-separated numbers
			parts := strings.Split(input, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if idx, err := strconv.Atoi(part); err == nil {
					if idx == 0 {
						// Custom answer
						fmt.Fprintf(h.Writer, "Enter your custom answer: ")
						if scanner.Scan() {
							selectedAnswers = append(selectedAnswers, strings.TrimSpace(scanner.Text()))
						}
					} else if idx > 0 && idx <= len(q.Options) {
						selectedAnswers = append(selectedAnswers, q.Options[idx-1].Label)
					}
				}
			}
		} else {
			// Single selection
			if idx, err := strconv.Atoi(input); err == nil {
				if idx == 0 {
					// Custom answer
					fmt.Fprintf(h.Writer, "Enter your custom answer: ")
					if scanner.Scan() {
						selectedAnswers = []string{strings.TrimSpace(scanner.Text())}
					}
				} else if idx > 0 && idx <= len(q.Options) {
					selectedAnswers = []string{q.Options[idx-1].Label}
				}
			} else if input != "" {
				// Treat non-numeric input as custom answer
				selectedAnswers = []string{input}
			}
		}

		answers[i] = selectedAnswers
	}

	return answers, nil
}

// NewStdioQuestionHandler creates a new stdio question handler.
func NewStdioQuestionHandler(r io.Reader, w io.Writer) *StdioQuestionHandler {
	return &StdioQuestionHandler{Reader: r, Writer: w}
}

// ScriptedQuestionHandler answers questions from a predefined queue.
// Used for replay testing with pre-recorded answers.
type ScriptedQuestionHandler struct {
	answers []string
	index   int
}

// NewScriptedQuestionHandler creates a handler with queued answers.
// Each answer should be either a 1-based option index ("1", "2", etc.)
// or a custom text answer.
func NewScriptedQuestionHandler(answers []string) *ScriptedQuestionHandler {
	return &ScriptedQuestionHandler{answers: answers}
}

func (h *ScriptedQuestionHandler) Ask(ctx context.Context, questions []bus.QuestionInfo) ([][]string, error) {
	answers := make([][]string, len(questions))

	for i, q := range questions {
		if h.index >= len(h.answers) {
			// No more scripted answers, return empty
			answers[i] = []string{}
			continue
		}

		input := h.answers[h.index]
		h.index++

		var selectedAnswers []string

		// Try to parse as number (1-based index)
		if idx, err := strconv.Atoi(input); err == nil && idx > 0 && idx <= len(q.Options) {
			selectedAnswers = []string{q.Options[idx-1].Label}
		} else {
			// Treat as custom answer
			selectedAnswers = []string{input}
		}

		answers[i] = selectedAnswers
	}

	return answers, nil
}
