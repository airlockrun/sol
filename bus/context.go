package bus

import "context"

// contextKey is an unexported type for context keys in this package.
type contextKey string

const (
	// permissionManagerKey is the context key for a scoped PermissionManager.
	permissionManagerKey contextKey = "bus.permissionManager"

	// questionManagerKey is the context key for a scoped QuestionManager.
	questionManagerKey contextKey = "bus.questionManager"

	// busKey is the context key for a scoped Bus.
	busKey contextKey = "bus.bus"
)

// WithPermissionManager returns a context with the given PermissionManager.
func WithPermissionManager(ctx context.Context, pm *PermissionManager) context.Context {
	return context.WithValue(ctx, permissionManagerKey, pm)
}

// PermissionManagerFromContext extracts a PermissionManager from context.
// Panics if not set — the runner must inject it via WithPermissionManager.
func PermissionManagerFromContext(ctx context.Context) *PermissionManager {
	if pm, ok := ctx.Value(permissionManagerKey).(*PermissionManager); ok && pm != nil {
		return pm
	}
	panic("bus: no PermissionManager in context — Runner must inject one via WithPermissionManager")
}

// WithQuestionManager returns a context with the given QuestionManager.
func WithQuestionManager(ctx context.Context, qm *QuestionManager) context.Context {
	return context.WithValue(ctx, questionManagerKey, qm)
}

// QuestionManagerFromContext extracts a QuestionManager from context.
// Panics if not set — the runner must inject it via WithQuestionManager.
func QuestionManagerFromContext(ctx context.Context) *QuestionManager {
	if qm, ok := ctx.Value(questionManagerKey).(*QuestionManager); ok && qm != nil {
		return qm
	}
	panic("bus: no QuestionManager in context — Runner must inject one via WithQuestionManager")
}

// WithBus returns a context with the given Bus.
func WithBus(ctx context.Context, b *Bus) context.Context {
	return context.WithValue(ctx, busKey, b)
}

// BusFromContext extracts a Bus from context.
// Panics if not set — the runner must inject it via WithBus.
func BusFromContext(ctx context.Context) *Bus {
	if b, ok := ctx.Value(busKey).(*Bus); ok && b != nil {
		return b
	}
	panic("bus: no Bus in context — Runner must inject one via WithBus")
}
