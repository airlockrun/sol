package sol

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/airlockrun/goai/tool"
)

const DefaultModelTimeout = 15 * time.Minute
const DefaultModelIdleTimeout = 5 * time.Minute
const DefaultToolTimeout = 10 * time.Minute
const DefaultMaintenanceTimeout = 60 * time.Second
const DefaultSettlementTimeout = 15 * time.Second

// ErrStepDeadline identifies a terminal foreground step deadline. A fresh user
// turn may proceed after settlement; interrupted effects must not be replayed.
var ErrStepDeadline = errors.New("foreground step deadline exceeded")

type stepExecutorKey struct{}

type stepExecutor struct {
	next       tool.Executor
	timeout    time.Duration
	stopStep   context.CancelCauseFunc
	mu         sync.Mutex
	dispatched map[string]bool
}

func (e *stepExecutor) Tools() []tool.Info { return e.next.Tools() }
func (e *stepExecutor) Execute(ctx context.Context, req tool.Request) (tool.Response, error) {
	if err := ctx.Err(); err != nil {
		return tool.Response{}, stepContextError(ctx)
	}
	e.mu.Lock()
	e.dispatched[req.ToolCallID] = true
	e.mu.Unlock()
	callCtx, cancel := context.WithTimeoutCause(ctx, e.timeout, errors.Join(ErrStepDeadline, errors.New("tool execution deadline exceeded")))
	defer cancel()
	resp, err := e.next.Execute(callCtx, req)
	if callCtx.Err() != nil {
		e.stopStep(stepContextError(callCtx))
		if err != nil {
			return tool.Response{}, errors.New(missingOutcome(callCtx, req.ToolCallID))
		}
	}
	return resp, err
}

func stepContextError(ctx context.Context) error {
	return errors.Join(ctx.Err(), context.Cause(ctx))
}

func missingOutcome(ctx context.Context, id string) string {
	reason := "stream interrupted"
	if errors.Is(context.Cause(ctx), ErrStepDeadline) {
		reason = "foreground step deadline exceeded"
	} else if errors.Is(ctx.Err(), context.Canceled) {
		reason = "run cancelled"
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		reason = "caller deadline exceeded"
	}
	if tracker, ok := ctx.Value(stepExecutorKey{}).(*stepExecutor); ok {
		tracker.mu.Lock()
		dispatched := tracker.dispatched[id]
		tracker.mu.Unlock()
		if !dispatched {
			return "Not dispatched: " + reason + "."
		}
	}
	return "Unknown outcome: " + reason + ". Cancellation does not undo effects. Inspect state before retrying; do not automatically replay this call."
}
