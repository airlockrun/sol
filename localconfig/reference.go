package localconfig

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// ProviderConfig binds one named account to exactly one authentication method.
// Empty credentials represent a disconnected account, never an environment fallback.
type ProviderConfig struct {
	Auth        string           `json:"auth"`
	Key         string           `json:"key,omitempty"`
	KeyEnv      string           `json:"keyEnv,omitempty"`
	Credentials *OAuthCredential `json:"credentials,omitempty"`
	BaseURL     string           `json:"baseURL,omitempty"`
}

// ModelRef retains the entire provider-specific model suffix, including slashes.
type ModelRef struct{ Entry, Provider, Model string }

func ParseEntry(entry string) (string, string, error) {
	parts := strings.Split(entry, "/")
	if len(parts) != 2 || !ValidID(parts[0]) || !ValidID(parts[1]) {
		return "", "", errors.New("sol config: provider entry must be slug/provider")
	}
	return parts[0], parts[1], nil
}

func ParseModelRef(value string) (ModelRef, error) {
	parts := strings.SplitN(value, "/", 3)
	if len(parts) != 3 || parts[2] == "" || strings.IndexFunc(parts[2], func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return ModelRef{}, errors.New("sol config: model reference must be slug/provider/model (for example work/openai/gpt-5.4)")
	}
	entry := parts[0] + "/" + parts[1]
	if _, _, err := ParseEntry(entry); err != nil {
		return ModelRef{}, err
	}
	return ModelRef{Entry: entry, Provider: parts[1], Model: parts[2]}, nil
}

// ValidID is case-sensitive and accepts [A-Za-z0-9][A-Za-z0-9._-]*.
func ValidID(id string) bool {
	for i, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '-' || c == '_' || c == '.') {
			continue
		}
		return false
	}
	return id != ""
}

func (c Config) Validate() error {
	if c.DefaultAgent != "" && !ValidID(c.DefaultAgent) {
		return errors.New("sol config: invalid default agent name")
	}
	if c.Providers == nil {
		return errors.New("sol config: providers map is required")
	}
	for entry, p := range c.Providers {
		_, provider, err := ParseEntry(entry)
		if err != nil {
			return err
		}
		if p.BaseURL != "" {
			u, err := url.Parse(p.BaseURL)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("sol config: baseURL must be an HTTP(S) endpoint without credentials, query or fragment")
			}
		}
		switch p.Auth {
		case APIKeyMode:
			if p.Credentials != nil || p.Key != "" && p.KeyEnv != "" {
				return errors.New("sol config: api-key requires at most one key or keyEnv and no OAuth credentials")
			}
			if p.Key != "" && (strings.TrimSpace(p.Key) == "" || strings.ContainsAny(p.Key, "\r\n")) {
				return errors.New("sol config: invalid API key")
			}
			if p.KeyEnv != "" && !ValidID(p.KeyEnv) {
				return errors.New("sol config: invalid keyEnv name")
			}
		case "codex":
			if provider != "openai" || p.Key != "" || p.KeyEnv != "" || p.BaseURL != "" {
				return errors.New("sol config: codex requires openai and no API key")
			}
			if p.Credentials != nil && (p.Credentials.AccessToken == "" || p.Credentials.RefreshToken == "" || p.Credentials.ExpiresAt.IsZero()) {
				return errors.New("sol config: incomplete OAuth credential")
			}
		default:
			return errors.New("sol config: auth must be api-key or codex")
		}
	}
	if c.DefaultModel != "" {
		_, _, err := c.Resolve(c.DefaultModel)
		return err
	}
	return nil
}

func (c Config) Resolve(value string) (ModelRef, ProviderConfig, error) {
	ref, err := ParseModelRef(value)
	if err != nil {
		return ModelRef{}, ProviderConfig{}, err
	}
	p, exists := c.Providers[ref.Entry]
	if !exists {
		return ModelRef{}, ProviderConfig{}, fmt.Errorf("sol config: unknown provider entry %s; configure it with sol auth set-key or sol auth login", ref.Entry)
	}
	return ref, p, nil
}

// EntryLocker is required for account-scoped credential mutations.
type EntryLocker interface {
	LockEntry(context.Context, string) (func(), error)
}
