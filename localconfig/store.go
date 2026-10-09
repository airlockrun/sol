// Package localconfig owns Sol's single-user configuration and credentials.
// It has no provider transport or project-directory discovery.
package localconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version is the current on-disk configuration schema version.
const Version = 2
const APIKeyMode = "api-key"
const maxStoreBytes = 1 << 20

// APIKeyCredential holds one explicitly configured provider key.
type APIKeyCredential struct {
	Key string `json:"key"`
}

// OAuthCredential holds refreshable tokens and optional account-routing metadata.
type OAuthCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	AccountID    string    `json:"account_id,omitempty"`
}

// ProviderAuth keeps API-key and named OAuth methods independently configurable.
type ProviderAuth struct {
	APIKey *APIKeyCredential          `json:"api_key,omitempty"`
	OAuth  map[string]OAuthCredential `json:"oauth,omitempty"`
}

// ModelSelection persists the model and deliberate authentication method together.
// Auth is api-key or a named OAuth method belonging to the selected provider.
type ModelSelection struct {
	Model string `json:"model"`
	Auth  string `json:"auth"`
}

// Config is the complete local configuration snapshot. Providers is initialized
// even when the file is absent. Credentials are not safe for display.
type Config struct {
	Providers    map[string]ProviderAuth `json:"providers"`
	DefaultModel *ModelSelection         `json:"default_model,omitempty"`
}

// Store serializes read-modify-write across its users, including token refresh.
// Update callbacks must not reenter the store. A failed callback is not persisted.
type Store interface {
	Load(context.Context) (Config, error)
	Update(context.Context, func(*Config) error) (Config, error)
}

// CommitStore supports transactions which accept an irreversible remote effect.
// Lock acquisition and the callback use the caller's context. The callback may
// supply a fresh bounded context for committing an accepted effect independently
// of caller cancellation. Ordinary configuration changes use Store.Update.
type CommitStore interface {
	Store
	UpdateWithCommit(context.Context, func(*Config) (context.Context, error)) (Config, error)
}

// DefaultPath resolves only the OS user config directory, never the project.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sol", "auth.json"), nil
}

// FileStore persists the complete record using private atomic writes and a stable
// advisory lock. Load and Update coordinate across independent processes.
type FileStore struct{ path string }

// NewFileStore requires an explicit absolute path in a Sol-owned directory.
// The containing directory is made private. Construction does not access files.
func NewFileStore(path string) (*FileStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("sol config: path must be absolute")
	}
	return &FileStore{path: filepath.Clean(path)}, nil
}

func (s *FileStore) Load(ctx context.Context) (Config, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return Config{}, err
	}
	defer unlock()
	return s.load()
}

func (s *FileStore) Update(ctx context.Context, change func(*Config) error) (Config, error) {
	if change == nil {
		return Config{}, errors.New("sol config: update callback is required")
	}
	return s.update(ctx, func(config *Config) (context.Context, error) { return ctx, change(config) })
}

// UpdateWithCommit requires a bounded commit context from a successful callback.
// Returning an error leaves the file unchanged. The caller owns context cleanup.
func (s *FileStore) UpdateWithCommit(ctx context.Context, change func(*Config) (context.Context, error)) (Config, error) {
	if change == nil {
		return Config{}, errors.New("sol config: update callback is required")
	}
	return s.update(ctx, func(config *Config) (context.Context, error) {
		commit, err := change(config)
		if err != nil {
			return nil, err
		}
		if commit == nil {
			return nil, errors.New("sol config: commit context is required")
		}
		if _, ok := commit.Deadline(); !ok {
			return nil, errors.New("sol config: commit context must be bounded")
		}
		return commit, nil
	})
}

func (s *FileStore) update(ctx context.Context, change func(*Config) (context.Context, error)) (Config, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return Config{}, err
	}
	defer unlock()
	config, err := s.load()
	if err != nil {
		return Config{}, err
	}
	before, err := encode(config)
	if err != nil {
		return Config{}, err
	}
	commit, err := change(&config)
	if err != nil {
		return Config{}, err
	}
	if err := commit.Err(); err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	data, err := encode(config)
	if err != nil {
		return Config{}, err
	}
	if len(data) > maxStoreBytes {
		return Config{}, errors.New("sol config: configuration is too large")
	}
	if string(before) == string(data) {
		return config, nil
	}
	if err := commit.Err(); err != nil {
		return Config{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".auth-*")
	if err != nil {
		return Config{}, err
	}
	defer os.Remove(f.Name())
	if err = privatePath(f.Name(), 0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return Config{}, err
	}
	if err := commit.Err(); err != nil {
		return Config{}, err
	}
	if err := replaceFile(f.Name(), s.path); err != nil {
		return Config{}, err
	}
	if err := syncDirectory(filepath.Dir(s.path)); err != nil {
		return Config{}, err
	}
	return config, nil
}

func encode(config Config) ([]byte, error) {
	return json.Marshal(struct {
		Version int `json:"version"`
		Config
	}{Version, config})
}

func (s *FileStore) load() (Config, error) {
	empty := Config{Providers: make(map[string]ProviderAuth)}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() {
		return Config{}, errors.New("sol config: file must be regular, not a symlink")
	}
	if err := checkPrivateFile(s.path, info); err != nil {
		return Config{}, err
	}
	f, err := os.Open(s.path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var record struct {
		Version int `json:"version"`
		Config
		Codex *OAuthCredential `json:"codex,omitempty"`
	}
	d := json.NewDecoder(io.LimitReader(f, maxStoreBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(&record); err != nil {
		return Config{}, errors.New("sol config: invalid configuration file")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("sol config: trailing configuration data")
	}
	switch record.Version {
	case 1:
		if record.Codex == nil || record.Providers != nil || record.DefaultModel != nil {
			return Config{}, errors.New("sol config: invalid version 1 record")
		}
		// Version 1 stores one Codex credential. Reading it into the shared record
		// preserves tokens; the next changed update writes the complete version 2.
		record.Config = Config{Providers: map[string]ProviderAuth{"openai": {OAuth: map[string]OAuthCredential{"codex": *record.Codex}}}}
	case Version:
		if record.Codex != nil {
			return Config{}, errors.New("sol config: invalid version 2 record")
		}
	default:
		return Config{}, fmt.Errorf("sol config: unsupported version %d", record.Version)
	}
	if err := record.Config.Validate(); err != nil {
		return Config{}, err
	}
	return record.Config, nil
}

// Validate checks typed records without exposing credential contents in errors.
func (c Config) Validate() error {
	if c.Providers == nil {
		return errors.New("sol config: providers map is required")
	}
	for id, provider := range c.Providers {
		if !ValidID(id) {
			return errors.New("sol config: invalid provider ID")
		}
		if provider.APIKey != nil && (strings.TrimSpace(provider.APIKey.Key) == "" || strings.ContainsAny(provider.APIKey.Key, "\r\n")) {
			return errors.New("sol config: invalid API key")
		}
		for method, credential := range provider.OAuth {
			if !ValidID(method) || method == APIKeyMode {
				return errors.New("sol config: invalid OAuth method")
			}
			if credential.AccessToken == "" || credential.RefreshToken == "" || credential.ExpiresAt.IsZero() {
				return errors.New("sol config: incomplete OAuth credential")
			}
		}
	}
	if c.DefaultModel != nil {
		return c.DefaultModel.Validate()
	}
	return nil
}

func (m ModelSelection) Validate() error {
	provider, model, ok := strings.Cut(m.Model, "/")
	if !ok || !ValidID(provider) || strings.TrimSpace(model) == "" || strings.ContainsAny(model, " \t\r\n") || !ValidID(m.Auth) {
		return errors.New("sol config: model requires provider/model and an auth method")
	}
	return nil
}

// ValidID checks provider and authentication-method identifiers.
func ValidID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func (s *FileStore) lock(ctx context.Context) (func(), error) {
	if s == nil || !filepath.IsAbs(s.path) {
		return nil, errors.New("sol config: use NewFileStore with an absolute path")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("sol config: directory must not be a symlink")
	}
	if err := privatePath(dir, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(s.path + ".lock"); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("sol config: lock file must be regular")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := privatePath(f.Name(), 0600); err != nil {
		f.Close()
		return nil, err
	}
	if err := lockFile(ctx, f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { unlockFile(f); f.Close() }, nil
}
