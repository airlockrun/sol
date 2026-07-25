package sol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
	"github.com/airlockrun/sol/session"
)

const (
	permissionDeniedReason   = "Execution was denied by the user."
	skippedAfterDeniedReason = "This tool was not executed because an earlier tool call in the same ordered batch was denied by the user."
)

// PermissionResolution contains the tool messages and results created while
// resolving one permission gate. SuspensionContext is non-nil when a later
// call in the ordered batch reaches another Sol suspension.
type PermissionResolution struct {
	Messages          []goai.Message
	Results           []stream.ToolResultEvent
	SuspensionContext *SuspensionContext
}

// ResolvePermissionSuspension resolves the current call in a permission
// suspension without invoking the model. Approval executes the current call
// with call-scoped permission and then executes later pending calls in order
// under the runner's normal managers. Denial records the current call as
// denied and every later call as skipped. Each result is attached or persisted
// before its stream event is published.
func (r *Runner) ResolvePermissionSuspension(ctx context.Context, suspension *SuspensionContext, approved bool) (*PermissionResolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	currentID, err := validatePermissionSuspension(suspension)
	if err != nil {
		return nil, err
	}

	if r.store != nil {
		history, err := r.store.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("store load before permission resolution: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !containsAssistantToolCall(history, currentID) {
			return nil, fmt.Errorf("store history does not contain pending assistant tool call %q", currentID)
		}
	} else if len(r.messages) > 0 {
		// A runner that produced the suspension already has the assistant tool
		// calls in r.messages. Make that snapshot the history for its next Run.
		r.initialMessages = append([]goai.Message(nil), r.messages...)
		r.messages = nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resolution := &PermissionResolution{}
	completed := append([]stream.ToolResultEvent(nil), suspension.CompletedResults...)
	pending := suspension.PendingToolCalls

	if !approved {
		for i, tc := range pending {
			reason := skippedAfterDeniedReason
			if i == 0 {
				reason = permissionDeniedReason
			}
			result := stream.ToolResultEvent{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Input:      tc.Input,
				Output:     message.ExecutionDeniedOutput{Reason: reason},
			}
			if err := r.attachPermissionResult(ctx, resolution, result); err != nil {
				return resolution, err
			}
		}
		return resolution, nil
	}

	executor := r.executor
	if executor == nil {
		executor = tool.NewLocalExecutor(r.toolSet, nil)
	}

	privateBus := bus.New()
	allowCurrent := bus.NewPermissionManager(privateBus)
	allowCurrent.AddRule(bus.PermissionRule{Permission: "*", Pattern: "*", Action: "allow"})
	currentCtx := bus.WithBus(ctx, privateBus)
	currentCtx = bus.WithPermissionManager(currentCtx, allowCurrent)
	currentCtx = bus.WithQuestionManager(currentCtx, r.questionMgr)

	normalCtx := bus.WithBus(ctx, r.bus)
	normalCtx = bus.WithPermissionManager(normalCtx, r.permissionMgr)
	normalCtx = bus.WithQuestionManager(normalCtx, r.questionMgr)

	for i, tc := range pending {
		if err := ctx.Err(); err != nil {
			return resolution, err
		}
		toolCtx := normalCtx
		if i == 0 && tc.ID == currentID {
			toolCtx = currentCtx
		}

		var gatePublished atomic.Bool
		unsubscribePermission := r.bus.Subscribe(bus.PermissionAsked, func(event bus.Event) {
			asked, ok := event.Properties.(bus.PermissionAskedPayload)
			if ok && asked.ToolCallID == tc.ID {
				gatePublished.Store(true)
			}
		})
		unsubscribeQuestion := r.bus.Subscribe(bus.QuestionAsked, func(event bus.Event) {
			asked, ok := event.Properties.(bus.QuestionAskedPayload)
			if ok && asked.Tool != nil && asked.Tool.CallID == tc.ID {
				gatePublished.Store(true)
			}
		})
		resp, execErr := executor.Execute(toolCtx, tool.Request{
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			Input:      tc.Input,
			SessionID:  r.sessionID,
			WorkDir:    r.workDir,
		})
		unsubscribePermission()
		unsubscribeQuestion()
		if execErr != nil {
			if err := ctx.Err(); err != nil {
				return resolution, err
			}
			if next, ok := suspensionContextFromError(execErr, pending[i:], completed); ok {
				if next.ToolCallID != tc.ID {
					return resolution, fmt.Errorf("tool %q suspended call %q while executing call %q", tc.Name, next.ToolCallID, tc.ID)
				}
				if !gatePublished.Load() {
					r.publishSuspensionGate(next)
				}
				resolution.SuspensionContext = next
				return resolution, nil
			}
			var fatal tool.FatalToolError
			if errors.As(execErr, &fatal) && fatal.FatalToolError() {
				return resolution, fmt.Errorf("execute tool %q: %w", tc.Name, execErr)
			}
		}

		result := stream.ToolResultEvent{
			ToolCallID: tc.ID,
			ToolName:   tc.Name,
			Input:      tc.Input,
		}
		switch {
		case execErr != nil:
			result.Output = tool.OutputForError(execErr)
		case resp.NoExecute:
			result.Output = message.ErrorTextOutput{Value: "No tool result was recorded for this call."}
		case resp.Denied:
			result.Output = message.ExecutionDeniedOutput{Reason: resp.DeniedReason}
		case resp.IsError:
			value := resp.Error
			if value == "" {
				value = resp.Output
			}
			result.Output = message.ErrorTextOutput{Value: value}
		default:
			result.Output = tool.SuccessOutput(tool.Result{
				Output:      resp.Output,
				Title:       resp.Title,
				Metadata:    resp.Metadata,
				Attachments: resp.Attachments,
			})
			result.Title = resp.Title
			result.Metadata = resp.Metadata
		}

		if err := r.attachPermissionResult(ctx, resolution, result); err != nil {
			return resolution, err
		}
		completed = append(completed, result)
		if err := ctx.Err(); err != nil {
			return resolution, err
		}

		if message.ToolOutcome(result.Output) == "denied" {
			for _, skipped := range pending[i+1:] {
				skippedResult := stream.ToolResultEvent{
					ToolCallID: skipped.ID,
					ToolName:   skipped.Name,
					Input:      skipped.Input,
					Output:     message.ExecutionDeniedOutput{Reason: skippedAfterDeniedReason},
				}
				if err := r.attachPermissionResult(ctx, resolution, skippedResult); err != nil {
					return resolution, err
				}
			}
			return resolution, nil
		}
	}

	return resolution, nil
}

func (r *Runner) publishSuspensionGate(suspension *SuspensionContext) {
	switch data := suspension.Data.(type) {
	case *bus.ErrPermissionNeeded:
		r.bus.Publish(bus.PermissionAsked, bus.PermissionAskedPayload{
			Permission: data.Permission,
			Patterns:   data.Patterns,
			ToolCallID: suspension.ToolCallID,
			Metadata:   data.Metadata,
		})
	case *bus.ErrQuestionNeeded:
		r.bus.Publish(bus.QuestionAsked, bus.QuestionAskedPayload{
			Questions: data.Questions,
			Tool:      &bus.ToolContext{CallID: suspension.ToolCallID},
		})
	}
}

func (r *Runner) attachPermissionResult(ctx context.Context, resolution *PermissionResolution, result stream.ToolResultEvent) error {
	msg := goai.NewToolMessage(result.ToolCallID, result.ToolName, result.Output)
	if r.store != nil {
		storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		err := r.store.Append(storeCtx, []session.Message{session.FromGoAIMessage(msg)})
		cancel()
		if err != nil {
			return fmt.Errorf("store append tool result %q: %w", result.ToolCallID, err)
		}
	} else {
		r.initialMessages = append(r.initialMessages, msg)
	}

	resolution.Messages = append(resolution.Messages, msg)
	resolution.Results = append(resolution.Results, result)
	r.bus.Publish(bus.StreamToolResult, result)
	return nil
}

func containsAssistantToolCall(history []session.Message, toolCallID string) bool {
	for _, msg := range history {
		if msg.Role != "assistant" {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type == "tool" && part.Tool != nil && part.Tool.CallID == toolCallID {
				return true
			}
		}
	}
	return false
}

func validatePermissionSuspension(suspension *SuspensionContext) (string, error) {
	if suspension == nil {
		return "", errors.New("permission suspension is required")
	}
	if suspension.Reason != "permission" {
		return "", fmt.Errorf("cannot resolve %q suspension as permission", suspension.Reason)
	}
	if len(suspension.PendingToolCalls) == 0 {
		return "", errors.New("permission suspension has no pending tool calls")
	}

	dataID, err := suspensionDataToolCallID(suspension.Data)
	if err != nil {
		return "", err
	}
	currentID := suspension.ToolCallID
	if currentID == "" {
		currentID = dataID
	}
	if currentID == "" {
		return "", errors.New("permission suspension has no tool call ID")
	}
	if dataID != "" && dataID != currentID {
		return "", fmt.Errorf("permission suspension tool call ID %q does not match data tool call ID %q", currentID, dataID)
	}
	if first := suspension.PendingToolCalls[0].ID; first != currentID {
		return "", fmt.Errorf("permission suspension current call %q is not first pending call %q", currentID, first)
	}
	return currentID, nil
}

func suspensionDataToolCallID(data any) (string, error) {
	if data == nil {
		return "", nil
	}
	switch v := data.(type) {
	case *bus.ErrPermissionNeeded:
		return v.ToolCallID, nil
	case *bus.ErrQuestionNeeded:
		return v.ToolCallID, nil
	case *bus.ErrDelegatedSuspend:
		return v.ToolCallID, nil
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode suspension data: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", fmt.Errorf("decode suspension data: %w", err)
	}
	for _, key := range []string{"toolCallID", "toolCallId"} {
		if value, ok := fields[key]; ok {
			var id string
			if err := json.Unmarshal(value, &id); err != nil {
				return "", fmt.Errorf("decode suspension data %s: %w", key, err)
			}
			return id, nil
		}
	}
	return "", nil
}

func suspensionContextFromError(err error, pending []stream.ToolCall, completed []stream.ToolResultEvent) (*SuspensionContext, bool) {
	var reason string
	var data any
	var toolCallID string

	var permErr *bus.ErrPermissionNeeded
	var questionErr *bus.ErrQuestionNeeded
	var delegatedErr *bus.ErrDelegatedSuspend
	switch {
	case errors.As(err, &permErr):
		reason, data, toolCallID = "permission", permErr, permErr.ToolCallID
	case errors.As(err, &questionErr):
		reason, data, toolCallID = "question", questionErr, questionErr.ToolCallID
	case errors.As(err, &delegatedErr):
		reason, data, toolCallID = "delegated", delegatedErr, delegatedErr.ToolCallID
	default:
		return nil, false
	}

	if toolCallID == "" && len(pending) > 0 {
		toolCallID = pending[0].ID
		switch value := data.(type) {
		case *bus.ErrPermissionNeeded:
			value.ToolCallID = toolCallID
		case *bus.ErrQuestionNeeded:
			value.ToolCallID = toolCallID
		case *bus.ErrDelegatedSuspend:
			value.ToolCallID = toolCallID
		}
	}

	return &SuspensionContext{
		Reason:           reason,
		Data:             data,
		ToolCallID:       toolCallID,
		PendingToolCalls: append([]stream.ToolCall(nil), pending...),
		CompletedResults: append([]stream.ToolResultEvent(nil), completed...),
	}, true
}
