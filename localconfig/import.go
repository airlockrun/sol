package localconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// importAuth imports recognized auth.json records only when config.json is absent.
// The source is preserved; config.json is the sole active configuration.
func (s *FileStore) importAuth(ctx context.Context, empty Config) (Config, error) {
	path := filepath.Join(filepath.Dir(s.path), "auth.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return Config{}, err
	}
	source := &FileStore{path: path}
	unlock, err := source.lock(ctx)
	if err != nil {
		return Config{}, err
	}
	defer unlock()
	info, err = os.Lstat(path)
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() {
		return Config{}, errors.New("sol config: import source must be regular")
	}
	if info.Size() > maxStoreBytes {
		return Config{}, errors.New("sol config: import source is too large")
	}
	if err := checkPrivateFile(path, info); err != nil {
		return Config{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var record struct {
		Version   int              `json:"version"`
		Codex     *OAuthCredential `json:"codex,omitempty"`
		Providers map[string]struct {
			APIKey *struct {
				Key string `json:"key"`
			} `json:"api_key,omitempty"`
			OAuth map[string]OAuthCredential `json:"oauth,omitempty"`
		} `json:"providers"`
		Default *struct {
			Model string `json:"model"`
			Auth  string `json:"auth"`
		} `json:"default_model,omitempty"`
	}
	d := json.NewDecoder(io.LimitReader(f, maxStoreBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(&record); err != nil {
		return Config{}, errors.New("sol config: invalid auth.json import source")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("sol config: trailing auth.json import data")
	}
	switch record.Version {
	case 1:
		if record.Codex == nil || record.Providers != nil || record.Default != nil {
			return Config{}, errors.New("sol config: invalid version 1 import")
		}
		empty.Providers["codex/openai"] = ProviderConfig{Auth: "codex", Credentials: record.Codex}
	case 2:
		if record.Codex != nil || record.Providers == nil {
			return Config{}, errors.New("sol config: invalid version 2 import")
		}
		for id, p := range record.Providers {
			if !ValidID(id) {
				return Config{}, errors.New("sol config: invalid imported provider")
			}
			if p.APIKey != nil {
				if strings.TrimSpace(p.APIKey.Key) == "" {
					return Config{}, errors.New("sol config: invalid imported API key")
				}
				empty.Providers["default/"+id] = ProviderConfig{Auth: APIKeyMode, Key: p.APIKey.Key}
			}
			for method, token := range p.OAuth {
				if method != "codex" || id != "openai" {
					return Config{}, errors.New("sol config: unsupported imported OAuth method")
				}
				empty.Providers["codex/openai"] = ProviderConfig{Auth: "codex", Credentials: &token}
			}
		}
		if record.Default != nil {
			slug := "default"
			switch record.Default.Auth {
			case APIKeyMode:
			case "codex":
				slug = "codex"
			default:
				return Config{}, errors.New("sol config: unsupported imported default auth")
			}
			empty.DefaultModel = slug + "/" + record.Default.Model
		}
	default:
		return Config{}, errors.New("sol config: unsupported auth.json import version")
	}
	if err := empty.Validate(); err != nil {
		return Config{}, err
	}
	data, err := encode(empty)
	if err != nil {
		return Config{}, err
	}
	if len(data) > maxStoreBytes {
		return Config{}, errors.New("sol config: imported configuration is too large")
	}
	if err := s.write(ctx, data); err != nil {
		return Config{}, err
	}
	return empty, nil
}
