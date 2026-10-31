package sol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/airlockrun/sol/bus"
)

// Questions returns the authoritative question contract in a durable suspension.
func (s *SuspensionContext) Questions() ([]bus.QuestionInfo, error) {
	if s == nil || s.Reason != "question" {
		return nil, errors.New("question suspension is required")
	}
	if _, err := validatePendingSuspension(s); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(s.Data)
	if err != nil {
		return nil, err
	}
	var data bus.ErrQuestionNeeded
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if len(data.Questions) == 0 {
		return nil, errors.New("question suspension has no questions")
	}
	return data.Questions, nil
}

// ValidateQuestionAnswers checks cardinality, labels and custom-answer authority.
func (s *SuspensionContext) ValidateQuestionAnswers(answers [][]string) error {
	questions, err := s.Questions()
	if err != nil {
		return err
	}
	if len(answers) != len(questions) {
		return errors.New("answer every question")
	}
	for i, question := range questions {
		if len(answers[i]) == 0 || (!question.Multiple && len(answers[i]) != 1) {
			return fmt.Errorf("invalid answer count for question %d", i+1)
		}
		seen := make(map[string]bool)
		for _, answer := range answers[i] {
			if strings.TrimSpace(answer) == "" || len(answer) > 16384 || seen[answer] {
				return fmt.Errorf("invalid answer for question %d", i+1)
			}
			seen[answer] = true
			valid := question.Custom
			for _, option := range question.Options {
				valid = valid || answer == option.Label
			}
			if !valid {
				return fmt.Errorf("unknown answer for question %d", i+1)
			}
		}
	}
	return nil
}

// ResolveQuestionSuspension answers only the current call, then dispatches the
// remaining ordered batch with normal question and permission gates.
func (r *Runner) ResolveQuestionSuspension(ctx context.Context, suspension *SuspensionContext, answers [][]string) (*PermissionResolution, error) {
	if err := suspension.ValidateQuestionAnswers(answers); err != nil {
		return nil, err
	}
	return r.resolveSuspension(ctx, suspension, true, answers)
}
