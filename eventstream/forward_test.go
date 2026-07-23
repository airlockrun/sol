package eventstream_test

import (
	"reflect"
	"testing"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/eventstream"
)

type recordingSink struct {
	calls    []string
	started  bus.AutomaticCompactionStartedPayload
	finished bus.AutomaticCompactionFinishedPayload
}

func (s *recordingSink) OnTextDelta(stream.TextDeltaEvent)            {}
func (s *recordingSink) OnToolCall(stream.ToolCallEvent)              {}
func (s *recordingSink) OnToolResult(stream.ToolResultEvent)          {}
func (s *recordingSink) OnPermissionAsked(bus.PermissionAskedPayload) {}
func (s *recordingSink) OnSuspension(*sol.SuspensionContext)          {}
func (s *recordingSink) OnAutomaticCompactionStarted(p bus.AutomaticCompactionStartedPayload) {
	s.calls = append(s.calls, "started")
	s.started = p
}
func (s *recordingSink) OnAutomaticCompactionFinished(p bus.AutomaticCompactionFinishedPayload) {
	s.calls = append(s.calls, "finished")
	s.finished = p
}

func TestForwardAutomaticCompactionLifecycle(t *testing.T) {
	b := bus.New()
	sink := &recordingSink{}
	unsubscribe := eventstream.Forward(b, sink)
	defer unsubscribe()

	b.Publish(bus.AutomaticCompactionStarted, bus.AutomaticCompactionStartedPayload{})
	b.Publish(bus.AutomaticCompactionFinished, bus.AutomaticCompactionFinishedPayload{
		TokensFreed: 42,
		Error:       "compaction failed",
	})

	if want := []string{"started", "finished"}; !reflect.DeepEqual(sink.calls, want) {
		t.Fatalf("calls = %v, want %v", sink.calls, want)
	}
	if sink.finished.TokensFreed != 42 || sink.finished.Error != "compaction failed" {
		t.Fatalf("finished = %+v", sink.finished)
	}
}
