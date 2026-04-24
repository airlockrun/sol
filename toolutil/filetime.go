package toolutil

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// FileTime is the global file time tracker instance.
var FileTime = &FileTimeTracker{
	sessions: make(map[string]map[string]time.Time),
}

// FileTimeTracker manages file read timestamps per session.
// This prevents overwriting files that have been modified externally
// since they were last read by the agent.
type FileTimeTracker struct {
	mu       sync.RWMutex
	sessions map[string]map[string]time.Time // sessionID -> filepath -> readTime
}

// Read records the current time as when a file was read
func (ft *FileTimeTracker) Read(sessionID, filepath string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if ft.sessions[sessionID] == nil {
		ft.sessions[sessionID] = make(map[string]time.Time)
	}
	ft.sessions[sessionID][filepath] = time.Now()
}

// Get returns when a file was last read in a session
func (ft *FileTimeTracker) Get(sessionID, filepath string) (time.Time, bool) {
	ft.mu.RLock()
	defer ft.mu.RUnlock()

	if ft.sessions[sessionID] == nil {
		return time.Time{}, false
	}
	t, ok := ft.sessions[sessionID][filepath]
	return t, ok
}

// Assert checks that a file hasn't been modified since it was last read.
// Returns an error if:
// - The file was never read (must read before writing)
// - The file was modified after it was read
func (ft *FileTimeTracker) Assert(sessionID, filepath string) error {
	readTime, ok := ft.Get(sessionID, filepath)
	if !ok {
		return fmt.Errorf("you must read file %s before overwriting it. Use the Read tool first", filepath)
	}

	info, err := os.Stat(filepath)
	if err != nil {
		// File doesn't exist anymore - that's ok for write
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	modTime := info.ModTime()
	if modTime.After(readTime) {
		return fmt.Errorf("file %s has been modified since it was last read.\nLast modification: %s\nLast read: %s\n\nPlease read the file again before modifying it",
			filepath,
			modTime.Format(time.RFC3339),
			readTime.Format(time.RFC3339),
		)
	}

	return nil
}

// Clear removes all tracked files for a session
func (ft *FileTimeTracker) Clear(sessionID string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	delete(ft.sessions, sessionID)
}
