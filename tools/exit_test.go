package tools

import "testing"

func TestExitStateSet(t *testing.T) {
	var s ExitState
	if s.Called() {
		t.Fatal("Called() true before Set")
	}
	if status, msg := s.Result(); status != "" || msg != "" {
		t.Fatalf("Result() = (%q, %q) before Set, want empty", status, msg)
	}

	s.Set("refused", "out of scope")

	if !s.Called() {
		t.Fatal("Called() false after Set")
	}
	status, msg := s.Result()
	if status != "refused" || msg != "out of scope" {
		t.Fatalf("Result() = (%q, %q), want (refused, out of scope)", status, msg)
	}

	// Set is last-write-wins.
	s.Set("success", "done")
	if status, msg := s.Result(); status != "success" || msg != "done" {
		t.Fatalf("Result() after re-Set = (%q, %q), want (success, done)", status, msg)
	}
}
