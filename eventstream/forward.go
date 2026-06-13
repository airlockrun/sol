// Package eventstream forwards typed bus events from a Sol run loop
// to a caller-supplied Sink. Consumers implement Sink to route events
// wherever they need — an HTTP/NDJSON response writer, a WebSocket
// publisher, an in-memory channel, a structured log, a TUI buffer.
//
// The Sink contract is the alignment point: only events listed on Sink
// are part of the stable wire surface. Adding a new event type to the
// bus is a breaking change to Sink, forcing every consumer to update
// before compile passes — no silent drift between, say, an HTTP-side
// translator and a UI-side translator that each subscribe to the bus
// independently.
package eventstream

import (
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/bus"
)

// Sink is the destination for translated bus events. Implementations
// own the wire shape (NDJSON, proto, plain text, …) and the
// destination (response writer, pub/sub, channel). Methods receive the
// typed payload directly — implementors do not type-assert against
// the raw bus.Event.
//
// Methods may be called concurrently from the bus's subscription
// goroutine; implementations are responsible for their own
// serialization (mutex, single-writer channel, etc.) when the
// underlying sink isn't thread-safe.
type Sink interface {
	// OnTextDelta carries one chunk of generated assistant text.
	OnTextDelta(stream.TextDeltaEvent)
	// OnToolCall signals the model has issued a tool invocation.
	OnToolCall(stream.ToolCallEvent)
	// OnToolResult carries the executor's response for a tool call.
	OnToolResult(stream.ToolResultEvent)
	// OnPermissionAsked carries a request to the human/UI for
	// approval before a gated tool runs. Implementations typically
	// surface this as a confirmation prompt.
	OnPermissionAsked(bus.PermissionAskedPayload)
	// OnSuspension carries the run-suspended snapshot the resume
	// path will consume. Implementations may serialize it to the
	// wire or use it to drive UI state.
	//
	// Suspension is emitted out-of-band, not through the bus: the
	// runner returns a SuspensionContext on RunResult and the
	// caller invokes sink.OnSuspension directly. Forward never
	// calls this method.
	OnSuspension(*sol.SuspensionContext)
}

// Forward subscribes sink to every relevant event published on b and
// returns the unsubscribe function from b.SubscribeAll — call it to
// stop forwarding (typically deferred for the lifetime of a run).
//
// Events not part of the Sink contract are dropped. Type-assertion
// mismatches on a known event type are also dropped (defensive — a
// future bus type change that breaks an event's payload shape
// shouldn't crash a long-running subscriber).
func Forward(b *bus.Bus, sink Sink) func() {
	return b.SubscribeAll(func(e bus.Event) {
		switch e.Type {
		case bus.StreamTextDelta:
			if v, ok := e.Properties.(stream.TextDeltaEvent); ok {
				sink.OnTextDelta(v)
			}
		case bus.StreamToolCall:
			if v, ok := e.Properties.(stream.ToolCallEvent); ok {
				sink.OnToolCall(v)
			}
		case bus.StreamToolResult:
			if v, ok := e.Properties.(stream.ToolResultEvent); ok {
				sink.OnToolResult(v)
			}
		case bus.PermissionAsked:
			if v, ok := e.Properties.(bus.PermissionAskedPayload); ok {
				sink.OnPermissionAsked(v)
			}
		}
	})
}
