package toolutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileTimeTracker_ReadAndGet(t *testing.T) {
	ft := &FileTimeTracker{
		sessions: make(map[string]map[string]time.Time),
	}

	sessionID := "test-session"
	filePath := "/some/file.txt"

	// Initially should not have a read time
	_, ok := ft.Get(sessionID, filePath)
	if ok {
		t.Error("expected no read time initially")
	}

	// Read the file
	ft.Read(sessionID, filePath)

	// Now should have a read time
	readTime, ok := ft.Get(sessionID, filePath)
	if !ok {
		t.Error("expected read time after Read()")
	}
	if time.Since(readTime) > time.Second {
		t.Error("read time should be recent")
	}
}

func TestFileTimeTracker_Assert_NotRead(t *testing.T) {
	ft := &FileTimeTracker{
		sessions: make(map[string]map[string]time.Time),
	}

	sessionID := "test-session"
	filePath := "/some/file.txt"

	// Assert should fail if file was never read
	err := ft.Assert(sessionID, filePath)
	if err == nil {
		t.Error("expected error when file was not read")
	}
	if err.Error() != "you must read file /some/file.txt before overwriting it. Use the Read tool first" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFileTimeTracker_Assert_FileModified(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")

	// Create initial file
	if err := os.WriteFile(filePath, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	ft := &FileTimeTracker{
		sessions: make(map[string]map[string]time.Time),
	}

	sessionID := "test-session"

	// Read the file
	ft.Read(sessionID, filePath)

	// Wait a moment and modify the file
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}

	// Assert should fail because file was modified
	err := ft.Assert(sessionID, filePath)
	if err == nil {
		t.Error("expected error when file was modified after read")
	}
	if err != nil && !strings.Contains(err.Error(), "has been modified since it was last read") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFileTimeTracker_Assert_FileNotModified(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")

	// Create initial file
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	ft := &FileTimeTracker{
		sessions: make(map[string]map[string]time.Time),
	}

	sessionID := "test-session"

	// Wait a moment to ensure read time is after file mtime
	time.Sleep(10 * time.Millisecond)

	// Read the file
	ft.Read(sessionID, filePath)

	// Assert should succeed because file was not modified
	err := ft.Assert(sessionID, filePath)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestFileTimeTracker_Assert_FileDeleted(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")

	// Create and then delete file
	if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	ft := &FileTimeTracker{
		sessions: make(map[string]map[string]time.Time),
	}

	sessionID := "test-session"
	ft.Read(sessionID, filePath)

	// Delete the file
	os.Remove(filePath)

	// Assert should succeed for deleted files (allows re-creating)
	err := ft.Assert(sessionID, filePath)
	if err != nil {
		t.Errorf("expected no error for deleted file, got: %v", err)
	}
}

func TestFileTimeTracker_Clear(t *testing.T) {
	ft := &FileTimeTracker{
		sessions: make(map[string]map[string]time.Time),
	}

	sessionID := "test-session"
	ft.Read(sessionID, "/file1.txt")
	ft.Read(sessionID, "/file2.txt")

	// Verify files are tracked
	_, ok1 := ft.Get(sessionID, "/file1.txt")
	_, ok2 := ft.Get(sessionID, "/file2.txt")
	if !ok1 || !ok2 {
		t.Error("files should be tracked before clear")
	}

	// Clear the session
	ft.Clear(sessionID)

	// Verify files are no longer tracked
	_, ok1 = ft.Get(sessionID, "/file1.txt")
	_, ok2 = ft.Get(sessionID, "/file2.txt")
	if ok1 || ok2 {
		t.Error("files should not be tracked after clear")
	}
}

func TestFileTimeTracker_MultipleSessions(t *testing.T) {
	ft := &FileTimeTracker{
		sessions: make(map[string]map[string]time.Time),
	}

	ft.Read("session1", "/file.txt")
	ft.Read("session2", "/other.txt")

	// Each session should only see its own files
	_, ok1 := ft.Get("session1", "/file.txt")
	_, ok2 := ft.Get("session1", "/other.txt")
	if !ok1 {
		t.Error("session1 should see /file.txt")
	}
	if ok2 {
		t.Error("session1 should not see /other.txt")
	}

	_, ok3 := ft.Get("session2", "/file.txt")
	_, ok4 := ft.Get("session2", "/other.txt")
	if ok3 {
		t.Error("session2 should not see /file.txt")
	}
	if !ok4 {
		t.Error("session2 should see /other.txt")
	}
}
