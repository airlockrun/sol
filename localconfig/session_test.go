package localconfig

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestSessionClearRetainsTranscriptAndIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	s, err := OpenSession(t.Context(), root, "", true)
	if err != nil {
		t.Fatal(err)
	}
	s.Record.Model, s.Record.Agent, s.Record.Title = "invalid-model", "plan", "original"
	s.Record.Active = true
	history := []session.Message{
		{ID: "user", Role: "user", Content: "original request"},
		{ID: "tool", Role: "tool", Content: "original tool result"},
		{ID: "answer", Role: "assistant", Content: "original answer"},
	}
	if err := s.Append(t.Context(), history); err != nil {
		t.Fatal(err)
	}
	before := s.Record
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Clear(canceled); err == nil || !reflect.DeepEqual(before, s.Record) {
		t.Fatal("canceled clear changed record")
	}
	for range 2 {
		if err := s.Clear(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = OpenSession(t.Context(), root, before.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Record.Active || len(s.Record.Context) != 0 || !reflect.DeepEqual(s.Record.History, history) {
		t.Fatalf("clear lost history or retained active context: %+v", s.Record)
	}
	if s.Record.ID != before.ID || s.Record.Model != before.Model || s.Record.Agent != before.Agent || s.Record.Title != before.Title || s.Record.WorkDir != before.WorkDir {
		t.Fatal("clear changed identity")
	}
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 0 {
		t.Fatal("cleared history returned to model", loaded, err)
	}
	newTurn := []session.Message{{ID: "new", Role: "user", Content: "fresh request"}}
	if err := s.Append(t.Context(), newTurn); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Record.Context, newTurn) || !reflect.DeepEqual(s.Record.History, append(history, newTurn...)) {
		t.Fatal("new turn duplicated or replaced full history")
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
