package bus

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// PermissionRequest represents a permission request.
type PermissionRequest struct {
	ID         string
	SessionID  string
	Permission string         // Permission type (e.g., "edit", "bash", "read")
	Patterns   []string       // Patterns being accessed (e.g., file paths)
	Always     []string       // Patterns to approve for "always" response
	Metadata   map[string]any // Additional metadata (e.g., diff for edits)
	ToolCallID string         // The tool call ID
}

// PermissionRule defines an auto-approve or auto-deny rule.
type PermissionRule struct {
	Permission string `json:"permission"` // Permission type to match (supports wildcards)
	Pattern    string `json:"pattern"`    // Pattern to match (supports wildcards)
	Action     string `json:"action"`     // "allow", "deny", or "ask"
}

// PermissionManager handles permission evaluation via rules.
// Non-blocking: Ask() returns immediately with either nil (allowed),
// PermissionDeniedError (denied by rule), or ErrPermissionNeeded (no rule matches).
type PermissionManager struct {
	mu    sync.RWMutex
	rules []PermissionRule
	bus   *Bus
}

// NewPermissionManager creates a new permission manager.
// The bus parameter is required — there is no global bus.
func NewPermissionManager(b *Bus) *PermissionManager {
	return &PermissionManager{
		rules: []PermissionRule{},
		bus:   b,
	}
}

// SetRules sets the permission rules.
func (pm *PermissionManager) SetRules(rules []PermissionRule) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.rules = rules
}

// AddRule adds a permission rule.
func (pm *PermissionManager) AddRule(rule PermissionRule) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.rules = append(pm.rules, rule)
}

// evaluateRules checks if any rule auto-approves or denies the request.
func (pm *PermissionManager) evaluateRules(permission, pattern string) string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	// Check rules in order (last matching rule wins)
	action := "ask"
	for _, rule := range pm.rules {
		if matchWildcard(permission, rule.Permission) && matchWildcard(pattern, rule.Pattern) {
			action = rule.Action
		}
	}
	return action
}

// Ask evaluates permission rules and returns immediately.
// Returns nil if all patterns are allowed, PermissionDeniedError if denied,
// or ErrPermissionNeeded if no rule matches (caller should suspend).
func (pm *PermissionManager) Ask(ctx context.Context, req PermissionRequest) error {
	// Always publish for observability
	pm.bus.Publish(PermissionAsked, PermissionAskedPayload{
		SessionID:  req.SessionID,
		Permission: req.Permission,
		Patterns:   req.Patterns,
		Always:     req.Always,
		Metadata:   req.Metadata,
		ToolCallID: req.ToolCallID,
	})

	for _, pattern := range req.Patterns {
		action := pm.evaluateRules(req.Permission, pattern)
		switch action {
		case "allow":
			continue
		case "deny":
			return &PermissionDeniedError{
				Permission: req.Permission,
				Pattern:    pattern,
				Reason:     "denied by rule",
			}
		default: // "ask" — no rule matches
			return &ErrPermissionNeeded{
				Permission: req.Permission,
				Patterns:   req.Patterns,
				ToolCallID: req.ToolCallID,
				Metadata:   req.Metadata,
			}
		}
	}
	return nil
}

// matchWildcard checks if a value matches a pattern with wildcard support.
func matchWildcard(value, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(value, prefix)
	}
	if strings.HasPrefix(pattern, "*") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(value, suffix)
	}
	return value == pattern
}

// PermissionDeniedError is returned when permission is denied by a rule.
type PermissionDeniedError struct {
	Permission string
	Pattern    string
	Reason     string
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("permission denied: %s for %s (%s)", e.Permission, e.Pattern, e.Reason)
}
