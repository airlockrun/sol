package session

import (
	"encoding/json"
	"testing"
)

func TestDoomLoopDetector_NoLoop(t *testing.T) {
	d := NewDoomLoopDetector()

	// Different tool calls should not trigger doom loop
	if d.RecordCall("bash", json.RawMessage(`{"command":"ls"}`)) {
		t.Error("Should not detect doom loop on first call")
	}
	if d.RecordCall("read", json.RawMessage(`{"path":"file.txt"}`)) {
		t.Error("Should not detect doom loop with different tool")
	}
	if d.RecordCall("bash", json.RawMessage(`{"command":"pwd"}`)) {
		t.Error("Should not detect doom loop with different args")
	}
}

func TestDoomLoopDetector_DetectsLoop(t *testing.T) {
	d := NewDoomLoopDetector()

	sameCall := json.RawMessage(`{"command":"ls -la"}`)

	// First two calls should not trigger
	if d.RecordCall("bash", sameCall) {
		t.Error("Should not detect doom loop on first call")
	}
	if d.RecordCall("bash", sameCall) {
		t.Error("Should not detect doom loop on second call")
	}

	// Third identical call should trigger
	if !d.RecordCall("bash", sameCall) {
		t.Error("Should detect doom loop on third identical call")
	}
}

func TestDoomLoopDetector_ResetClearsHistory(t *testing.T) {
	d := NewDoomLoopDetector()

	sameCall := json.RawMessage(`{"command":"ls"}`)

	// Build up to threshold
	d.RecordCall("bash", sameCall)
	d.RecordCall("bash", sameCall)

	// Reset
	d.Reset()

	// Should need 3 more calls to trigger
	if d.RecordCall("bash", sameCall) {
		t.Error("Should not detect doom loop after reset")
	}
	if d.RecordCall("bash", sameCall) {
		t.Error("Should not detect doom loop after reset (2)")
	}
	if !d.RecordCall("bash", sameCall) {
		t.Error("Should detect doom loop after 3 new calls")
	}
}

func TestDoomLoopDetector_DifferentArgBreaksLoop(t *testing.T) {
	d := NewDoomLoopDetector()

	// Two identical calls
	d.RecordCall("bash", json.RawMessage(`{"command":"ls"}`))
	d.RecordCall("bash", json.RawMessage(`{"command":"ls"}`))

	// Different arg breaks the pattern
	if d.RecordCall("bash", json.RawMessage(`{"command":"pwd"}`)) {
		t.Error("Different args should break the loop pattern")
	}

	// Even with same tool and args again, need 3 new identical
	if d.RecordCall("bash", json.RawMessage(`{"command":"ls"}`)) {
		t.Error("Should not detect loop yet")
	}
}

func TestDoomLoopDetector_LastToolName(t *testing.T) {
	d := NewDoomLoopDetector()

	if d.LastToolName() != "" {
		t.Error("LastToolName should be empty initially")
	}

	d.RecordCall("bash", json.RawMessage(`{}`))
	if d.LastToolName() != "bash" {
		t.Errorf("LastToolName should be 'bash', got %q", d.LastToolName())
	}

	d.RecordCall("read", json.RawMessage(`{}`))
	if d.LastToolName() != "read" {
		t.Errorf("LastToolName should be 'read', got %q", d.LastToolName())
	}
}
