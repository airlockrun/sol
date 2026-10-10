package localconfig

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/airlockrun/sol/session"
)

// LocalSession records both the complete transcript and the compacted model context.
type LocalSession struct {
	Version int               `json:"version"`
	ID      string            `json:"id"`
	Title   string            `json:"title"`
	WorkDir string            `json:"workDir"`
	Model   string            `json:"model"`
	Agent   string            `json:"agent"`
	Updated time.Time         `json:"updated"`
	Active  bool              `json:"active"`
	History []session.Message `json:"history"`
	Context []session.Message `json:"context"`
}

// SessionPath uses private per-user state, independent of terminal selection.
func SessionPath() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(root) {
		return "", errors.New("XDG_STATE_HOME must be absolute")
	}
	return filepath.Join(root, "sol", "sessions"), nil
}

// SessionFile holds the cross-process lease for one conversation until Close.
type SessionFile struct {
	mu     sync.Mutex
	file   *FileStore
	unlock func()
	Record LocalSession
}

func OpenSession(ctx context.Context, root, id string, create bool) (*SessionFile, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("session root must be absolute")
	}
	if create && id == "" {
		id = rand.Text()
	}
	if !ValidID(id) || len(id) > 128 {
		return nil, errors.New("invalid session ID")
	}
	f, err := NewFileStore(filepath.Join(root, id+".json"))
	if err != nil {
		return nil, err
	}
	lockCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	unlock, err := f.lock(lockCtx)
	if err != nil {
		return nil, fmt.Errorf("session %s is busy or unavailable: %w", id, err)
	}
	s := &SessionFile{file: f, unlock: unlock}
	info, err := os.Lstat(f.path)
	if errors.Is(err, os.ErrNotExist) && create {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			unlock()
			return nil, cwdErr
		}
		s.Record = LocalSession{Version: 1, ID: id, WorkDir: cwd}
		err = s.Save(ctx)
	} else if err == nil {
		if !info.Mode().IsRegular() {
			err = errors.New("session must be a regular file")
		} else {
			err = checkPrivateFile(f.path, info)
		}
		if err == nil {
			var data []byte
			data, err = os.ReadFile(f.path)
			if err == nil {
				err = json.Unmarshal(data, &s.Record)
			}
		}
		if err == nil && (s.Record.Version != 1 || s.Record.ID != id) {
			err = errors.New("invalid session record")
		}
	}
	if err != nil {
		unlock()
		return nil, err
	}
	return s, nil
}

func (s *SessionFile) Close() { s.unlock() }
func (s *SessionFile) Save(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.Record.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(s.Record, "", "  ")
	if err != nil {
		return err
	}
	return s.file.write(ctx, data)
}
func (s *SessionFile) Load(ctx context.Context) ([]session.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Copy through the wire representation so runner compaction cannot mutate history.
	data, err := json.Marshal(s.Record.Context)
	if err != nil {
		return nil, err
	}
	var messages []session.Message
	err = json.Unmarshal(data, &messages)
	return messages, err
}
func (s *SessionFile) Append(ctx context.Context, messages []session.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Record.History = append(s.Record.History, messages...)
	s.Record.Context = append(s.Record.Context, messages...)
	return s.Save(ctx)
}
func (s *SessionFile) Compact(ctx context.Context, messages []session.Message, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Record.Context = messages
	return s.Save(ctx)
}

func ListSessions(root string) ([]LocalSession, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []LocalSession{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []LocalSession
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("session must be a regular file")
		}
		if err := checkPrivateFile(path, info); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var record LocalSession
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		if record.Version != 1 || entry.Name() != record.ID+".json" {
			return nil, errors.New("invalid session record")
		}
		record.History, record.Context = nil, nil
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Updated.After(records[j].Updated) })
	return records, nil
}
