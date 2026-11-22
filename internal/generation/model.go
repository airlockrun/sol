// Package generation bounds model attempts independently of tool execution.
package generation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/airlockrun/goai/stream"
)

const DefaultWall = 15 * time.Minute
const DefaultIdle = 5 * time.Minute

// ErrDeadline identifies a terminal model or foreground step deadline.
var ErrDeadline = errors.New("foreground step deadline exceeded")

// Model bounds provider work independently of tool execution. The idle
// clock runs only while waiting for the provider, not while delivering output.
// A provider's returned channel must close after its request work settles,
// including on cancellation. Cancellation alone is not a completion signal.
// A synchronous Stream error must already have settled its request work.
type Model struct {
	stream.Model
	Wall, Idle time.Duration
	StopStep   context.CancelCauseFunc
	Producers  sync.WaitGroup
}

func (m *Model) SetupMaxRetries() (int, bool) {
	if configured, ok := m.Model.(interface{ SetupMaxRetries() (int, bool) }); ok {
		return configured.SetupMaxRetries()
	}
	return 0, false
}

func (m *Model) Stream(parent context.Context, options *stream.CallOptions) (<-chan stream.Event, error) {
	m.Producers.Add(1)
	wallCtx, wallCancel := context.WithTimeoutCause(parent, m.Wall, errors.Join(ErrDeadline, errors.New("model response wall deadline exceeded")))
	ctx, cancel := context.WithCancelCause(wallCtx)
	idle := newModelIdleClock(m.Idle, func() {
		cancel(errors.Join(context.DeadlineExceeded, ErrDeadline, errors.New("model response inactivity deadline exceeded")))
	})
	cleanup := func() { idle.stop(); cancel(nil); wallCancel() }
	failure := func() error {
		err := errors.Join(ctx.Err(), context.Cause(ctx))
		if errors.Is(err, ErrDeadline) {
			m.StopStep(err)
		}
		return err
	}
	inner, err := m.Model.Stream(ctx, options)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(err, failure())
		}
		cleanup()
		m.Producers.Done()
		return nil, err
	}
	if inner == nil {
		cleanup()
		m.Producers.Done()
		return nil, errors.New("model returned nil event stream")
	}
	out := make(chan stream.Event)
	emitFailure := func() {
		event := stream.Event{Type: stream.EventError, Data: stream.ErrorEvent{Error: failure()}}
		select {
		case out <- event:
		case <-parent.Done():
		}
	}
	go func() {
		defer m.Producers.Done()
		defer close(out)
		defer cleanup()
		for {
			select {
			case <-ctx.Done():
				// Join the producer before ownership can pass to another writer.
				for range inner {
				}
				emitFailure()
				return
			case event, ok := <-inner:
				if !ok {
					if ctx.Err() != nil {
						emitFailure()
					}
					return
				}
				if failureEvent, failed := event.Data.(stream.ErrorEvent); failed {
					if ctx.Err() != nil {
						failureEvent.Error = errors.Join(failureEvent.Error, failure())
						event.Data = failureEvent
					}
					cancel(nil)
					for range inner {
					}
					select {
					case out <- event:
					case <-parent.Done():
					}
					return
				}
				idle.pause(Progress(event))
				select {
				case out <- event:
				case <-ctx.Done():
					for range inner {
					}
					emitFailure()
					return
				}
				idle.resume()
			}
		}
	}()
	return out, nil
}

func Progress(event stream.Event) bool {
	switch e := event.Data.(type) {
	case stream.TextDeltaEvent:
		return e.Text != ""
	case stream.ReasoningDeltaEvent:
		return e.Text != ""
	case stream.ToolInputDeltaEvent:
		return e.Delta != ""
	case stream.ReasoningEndEvent:
		return len(e.ProviderMetadata) > 0
	case stream.ToolCallEvent, stream.ToolResultEvent, stream.SourceEvent, stream.FinishEvent, stream.FinishStepEvent:
		return true
	default:
		return false
	}
}

type modelIdleClock struct {
	mu              sync.Mutex
	timer           *time.Timer
	idle, remaining time.Duration
	deadline        time.Time
	armed, closed   bool
	expire          func()
}

func newModelIdleClock(idle time.Duration, expire func()) *modelIdleClock {
	c := &modelIdleClock{idle: idle, remaining: idle, deadline: time.Now().Add(idle), armed: true, expire: expire}
	c.mu.Lock()
	c.timer = time.AfterFunc(idle, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || !c.armed {
			return
		}
		if left := time.Until(c.deadline); left > 0 {
			c.timer.Reset(left)
			return
		}
		c.expire()
	})
	c.mu.Unlock()
	return c
}

func (c *modelIdleClock) pause(progress bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remaining = time.Until(c.deadline)
	if c.remaining <= 0 {
		c.expire()
	}
	c.armed = false
	c.timer.Stop()
	if progress {
		c.remaining = c.idle
	}
}

func (c *modelIdleClock) resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.deadline = time.Now().Add(c.remaining)
	c.armed = true
	c.timer.Reset(c.remaining)
}

func (c *modelIdleClock) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.timer.Stop()
}
