package session

import (
	"context"
	"encoding/json"

	"github.com/airlockrun/sol/bus"
)

const (
	// DoomLoopThreshold is the number of identical tool calls before triggering detection.
	DoomLoopThreshold = 3
)

// ToolCallRecord represents a tool call for doom loop detection.
type ToolCallRecord struct {
	Name  string `json:"name"`
	Input string `json:"input"` // JSON string for comparison
}

// DoomLoopDetector tracks recent tool calls to detect repetitive patterns.
type DoomLoopDetector struct {
	recentCalls []ToolCallRecord
}

// NewDoomLoopDetector creates a new doom loop detector.
func NewDoomLoopDetector() *DoomLoopDetector {
	return &DoomLoopDetector{
		recentCalls: make([]ToolCallRecord, 0, DoomLoopThreshold),
	}
}

// RecordCall records a tool call and returns true if a doom loop is detected.
func (d *DoomLoopDetector) RecordCall(name string, input json.RawMessage) bool {
	// Convert input to string for comparison
	inputStr := string(input)

	record := ToolCallRecord{
		Name:  name,
		Input: inputStr,
	}

	// Add to recent calls
	d.recentCalls = append(d.recentCalls, record)

	// Keep only last N calls
	if len(d.recentCalls) > DoomLoopThreshold {
		d.recentCalls = d.recentCalls[len(d.recentCalls)-DoomLoopThreshold:]
	}

	// Check for doom loop
	return d.isLooping()
}

// isLooping checks if the last N calls are identical.
func (d *DoomLoopDetector) isLooping() bool {
	if len(d.recentCalls) < DoomLoopThreshold {
		return false
	}

	// Get the last call as reference
	last := d.recentCalls[len(d.recentCalls)-1]

	// Check if all recent calls are identical
	for i := len(d.recentCalls) - DoomLoopThreshold; i < len(d.recentCalls); i++ {
		call := d.recentCalls[i]
		if call.Name != last.Name || call.Input != last.Input {
			return false
		}
	}

	return true
}

// Reset clears the recorded calls (call after user confirms to continue).
func (d *DoomLoopDetector) Reset() {
	d.recentCalls = d.recentCalls[:0]
}

// LastToolName returns the name of the last recorded tool.
func (d *DoomLoopDetector) LastToolName() string {
	if len(d.recentCalls) == 0 {
		return ""
	}
	return d.recentCalls[len(d.recentCalls)-1].Name
}

// AskDoomLoopPermission asks the user if they want to continue despite the doom loop.
// Returns nil if user approves, error if rejected.
// Uses the context-scoped permission manager if available, otherwise the global.
func AskDoomLoopPermission(ctx context.Context, sessionID, toolName string) error {
	pm := bus.PermissionManagerFromContext(ctx)
	return pm.Ask(ctx, bus.PermissionRequest{
		SessionID:  sessionID,
		Permission: "doom_loop",
		Patterns:   []string{toolName},
		Metadata: map[string]any{
			"message": "The model is calling the same tool repeatedly with identical arguments",
		},
	})
}
