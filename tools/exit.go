package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/airlockrun/goai/tool"
)

// ExitState captures the outcome reported by the agent via the exit tool.
// Pass a pointer into RunnerOptions.ExitState to opt the runner into
// "must call exit" termination semantics; the runner breaks the step loop
// as soon as the tool stores a result.
//
// Designed for autonomous, non-interactive runs (CI, builds, sol-CLI piping)
// where a structured outcome is more useful than parsing the model's text.
type ExitState struct {
	mu      sync.Mutex
	called  bool
	status  string // "success" or "error"
	message string
}

// Called reports whether the exit tool has been invoked at least once.
func (s *ExitState) Called() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

// Result returns the last (status, message) the agent passed to exit.
// Zero values when Called() is false.
func (s *ExitState) Result() (status, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.message
}

type exitInput struct {
	Status  string `json:"status" description:"\"success\" if the task was completed, \"error\" if it could not be completed"`
	Message string `json:"message" description:"On success: a brief summary of what was done. On error: a clear explanation of why the task could not be completed"`
}

// ExitTool returns the tool the agent must call to end the run. The tool
// stores the agent-provided status/message into state and returns a benign
// success result so the model isn't surprised mid-stream; the runner loop
// observes ExitState after the step completes and terminates with
// RunStatus = RunExited.
//
// Description doubles as the spec the model reads — it explicitly says
// "you must call this tool exactly once as your final action."
func ExitTool(state *ExitState) tool.Tool {
	if state == nil {
		panic("sol/tools: ExitTool called with nil state")
	}
	return tool.New("exit").
		Description(`Call this tool exactly once, as your final action, when you finish your task or determine you cannot finish it.

Set status="success" with a one-paragraph summary of what you did when the task was completed.
Set status="error" with a clear explanation of the blocker when you cannot complete the task.

After you call this tool the run terminates immediately. Do NOT output additional text or call any other tool after exit.`).
		SchemaFromStruct(exitInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var p exitInput
			if err := json.Unmarshal(input, &p); err != nil {
				return tool.Result{}, fmt.Errorf("exit: invalid input: %w", err)
			}
			if p.Status != "success" && p.Status != "error" {
				return tool.Result{}, fmt.Errorf("exit: status must be \"success\" or \"error\", got %q", p.Status)
			}
			state.mu.Lock()
			state.called = true
			state.status = p.Status
			state.message = p.Message
			state.mu.Unlock()
			return tool.Result{
				Output: "Run terminated. The runner will return RunExited.",
				Title:  "exit:" + p.Status,
			}, nil
		}).
		Build()
}
