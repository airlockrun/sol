package executor

import (
	"context"
	"errors"
)

// CallQuestionAnswersCapability identifies request-scoped question resumption.
const CallQuestionAnswersCapability = "call-question-answers.v1"

func (t *WSTransport) RequireCallQuestionAnswers(ctx context.Context) error {
	response, err := t.roundTrip(ctx, message{Type: "tools", ID: t.allocID()})
	if err != nil {
		return err
	}
	for _, capability := range response.Capabilities {
		if capability == CallQuestionAnswersCapability {
			return nil
		}
	}
	return errors.New("toolserver requires rebuild: missing call-question-answers.v1; rebuild the agent-builder image with matching Sol source and recreate workspace toolservers")
}
