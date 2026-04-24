package bus

import (
	"context"
	"strconv"
	"sync"
)

// QuestionManager handles question ask/reply lifecycle.
// Non-blocking: Ask() checks pre-loaded answers or auto-answer mode,
// returning ErrQuestionNeeded if no answer is available.
type QuestionManager struct {
	mu              sync.Mutex
	answers         [][][]string // FIFO queue of pre-loaded answer batches
	scriptedAnswers []string     // FIFO queue of raw scripted answers (resolved at ask time)
	autoAnswer      bool         // return empty answers when queue exhausted
	bus             *Bus
}

// NewQuestionManager creates a new question manager.
// The bus parameter is required — there is no global bus.
func NewQuestionManager(b *Bus) *QuestionManager {
	return &QuestionManager{
		bus: b,
	}
}

// AskInput is the input for asking questions.
type AskInput struct {
	SessionID string
	Questions []QuestionInfo
	Tool      *ToolContext
}

// Ask checks pre-loaded answers or auto-answer mode.
// Returns ErrQuestionNeeded if no answer is available (caller should suspend).
func (qm *QuestionManager) Ask(ctx context.Context, input AskInput) ([][]string, error) {
	// Always publish for observability
	qm.bus.Publish(QuestionAsked, QuestionAskedPayload{
		SessionID: input.SessionID,
		Questions: input.Questions,
		Tool:      input.Tool,
	})

	// Check pre-loaded answers (already resolved)
	qm.mu.Lock()
	if len(qm.answers) > 0 {
		answers := qm.answers[0]
		qm.answers = qm.answers[1:]
		qm.mu.Unlock()
		return answers, nil
	}

	// Check scripted answers (resolved against options at ask time)
	if len(qm.scriptedAnswers) > 0 {
		answers := make([][]string, len(input.Questions))
		for i, q := range input.Questions {
			if len(qm.scriptedAnswers) == 0 {
				answers[i] = []string{}
				continue
			}
			raw := qm.scriptedAnswers[0]
			qm.scriptedAnswers = qm.scriptedAnswers[1:]

			// Try to parse as 1-based option index
			if idx, err := strconv.Atoi(raw); err == nil && idx > 0 && idx <= len(q.Options) {
				answers[i] = []string{q.Options[idx-1].Label}
			} else {
				answers[i] = []string{raw}
			}
		}
		qm.mu.Unlock()
		return answers, nil
	}
	qm.mu.Unlock()

	// Auto-answer mode
	if qm.autoAnswer {
		answers := make([][]string, len(input.Questions))
		for i := range answers {
			answers[i] = []string{}
		}
		return answers, nil
	}

	// No answer → suspend
	return nil, &ErrQuestionNeeded{
		Questions: input.Questions,
	}
}

// SetAutoAnswer enables or disables auto-answer mode.
// When enabled, empty answers are returned when the queue is exhausted.
func (qm *QuestionManager) SetAutoAnswer(auto bool) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.autoAnswer = auto
}

// PushAnswers adds a batch of pre-resolved answers to the FIFO queue.
func (qm *QuestionManager) PushAnswers(answers [][]string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.answers = append(qm.answers, answers)
}

// PushScriptedAnswers adds raw string answers that get resolved at ask time.
// Numeric strings (e.g. "6") are resolved to the corresponding 1-based option label.
// Non-numeric strings are used as-is (custom answers).
func (qm *QuestionManager) PushScriptedAnswers(answers []string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.scriptedAnswers = append(qm.scriptedAnswers, answers...)
}
