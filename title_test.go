package sol

import (
	"context"
	"errors"
	"testing"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/testutil"
)

func TestGenerateTitleWithModelAsync(t *testing.T) {
	model := newMockModel(t, testutil.MockConfig{ID: "fixture",
		Default: &testutil.MockResponse{Events: testutil.MockTextResponse(" A title ", testutil.MockUsage(1, 1))}})
	result := <-GenerateTitleWithModelAsync(t.Context(), "prompt", model)
	if result.Error != nil || result.Title != "A title" {
		t.Fatal(result)
	}
	if len(model.Requests()) != 1 || model.Requests()[0].Messages[0].Role != "system" {
		t.Fatal("injected model not used")
	}
	if result := <-GenerateTitleWithModelAsync(t.Context(), "prompt", nil); result.Error == nil {
		t.Fatal("accepted nil model")
	}
}

type failingTitleModel struct{ failure error }

func (m failingTitleModel) ID() string       { return "test" }
func (m failingTitleModel) Provider() string { return "test" }
func (m failingTitleModel) Stream(context.Context, *stream.CallOptions) (<-chan stream.Event, error) {
	events := make(chan stream.Event, 1)
	events <- stream.Event{Type: stream.EventError, Data: stream.ErrorEvent{Error: m.failure}}
	close(events)
	return events, nil
}

func TestGenerateTitleWithModelAsyncError(t *testing.T) {
	failure := errors.New("title failed")
	result := <-GenerateTitleWithModelAsync(t.Context(), "prompt", failingTitleModel{failure})
	if !errors.Is(result.Error, failure) {
		t.Fatal(result)
	}
}
