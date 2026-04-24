package tools

import (
	"context"

	"github.com/airlockrun/sol/bus"
)

// Re-export error types from bus
type (
	PermissionDeniedError = bus.PermissionDeniedError
)

// PermissionRequest is a local type that maps to bus.PermissionRequest.
type PermissionRequest struct {
	ID         string
	SessionID  string
	Permission string
	Patterns   []string
	Always     []string
	Metadata   map[string]any
	ToolCallID string
}

// AskPermission requests permission through the context-scoped permission manager.
// The runner must have injected a PermissionManager into the context.
func AskPermission(ctx context.Context, req PermissionRequest) error {
	pm := bus.PermissionManagerFromContext(ctx)
	return pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  req.SessionID,
		Permission: req.Permission,
		Patterns:   req.Patterns,
		Always:     req.Always,
		Metadata:   req.Metadata,
		ToolCallID: req.ToolCallID,
	})
}
