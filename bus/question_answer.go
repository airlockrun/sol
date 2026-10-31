package bus

import "context"

type questionAnswerKey struct{}

// CallQuestionAnswers binds one answer batch to one tool invocation. Remote
// executors carry this binding on that request, not in a shared answer queue.
type CallQuestionAnswers struct {
	ToolCallID string     `json:"toolCallID"`
	Answers    [][]string `json:"answers"`
}

func WithCallQuestionAnswers(ctx context.Context, answer CallQuestionAnswers) context.Context {
	return context.WithValue(ctx, questionAnswerKey{}, answer)
}

func QuestionAnswersForCall(ctx context.Context, callID string) ([][]string, bool) {
	answer, ok := ctx.Value(questionAnswerKey{}).(CallQuestionAnswers)
	return answer.Answers, ok && answer.ToolCallID == callID
}
