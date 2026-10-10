package localconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/airlockrun/sol/session"
)

func TestSessionPersistenceCompactionAndLease(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	s, err := OpenSession(t.Context(), root, "", true)
	if err != nil {
		t.Fatal(err)
	}
	id := s.Record.ID
	if _, err := OpenSession(t.Context(), root, id, false); err == nil {
		t.Fatal("concurrent session admitted")
	}
	other, err := OpenSession(t.Context(), root, "", true)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	msgs := []session.Message{{ID: "first", Role: "user", Content: "keep original"}}
	if err := s.Append(t.Context(), msgs); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(t.Context(), []session.Message{{ID: "summary", Role: "assistant", Content: "summary", Summary: true}}, 10); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenSession(t.Context(), root, id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Record.History[0].Content != "keep original" || s.Record.Context[0].Content != "summary" {
		t.Fatal("compaction discarded history or failed to update context")
	}
	loaded, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	loaded[0].Content = "mutated"
	if s.Record.Context[0].Content != "summary" {
		t.Fatal("load aliased durable context")
	}
	before, _ := os.ReadFile(s.file.path)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Compact(ctx, nil, 0); err == nil {
		t.Fatal("canceled write succeeded")
	}
	after, _ := os.ReadFile(s.file.path)
	if string(before) != string(after) {
		t.Fatal("canceled write replaced durable context")
	}
	var record LocalSession
	if err := json.Unmarshal(after, &record); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRejectsUnknownAndTraversal(t *testing.T) {
	for _, id := range []string{"unknown", "../escape", "", ".hidden", "a/b"} {
		t.Run(id, func(t *testing.T) {
			if _, err := OpenSession(t.Context(), t.TempDir(), id, false); err == nil {
				t.Fatal("invalid session opened")
			}
		})
	}
}
