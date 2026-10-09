package codex

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/airlockrun/sol/localconfig"
)

func TestCredentialStorePreservesSharedConfiguration(t *testing.T) {
	shared, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = shared.Update(t.Context(), func(config *localconfig.Config) error {
		config.DefaultModel = &localconfig.ModelSelection{Model: "openai/gpt-5.4", Auth: "codex"}
		config.Providers["openai"] = localconfig.ProviderAuth{APIKey: &localconfig.APIKeyCredential{Key: "openai-test-key"}, OAuth: map[string]localconfig.OAuthCredential{"other": {AccessToken: "other", RefreshToken: "other", ExpiresAt: time.Now().Add(time.Hour)}}}
		config.Providers["anthropic"] = localconfig.ProviderAuth{APIKey: &localconfig.APIKeyCredential{Key: "anthropic-test-key"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(shared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(t.Context()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatal(err)
	}
	for _, action := range []string{"login", "refresh", "logout"} {
		t.Run(action, func(t *testing.T) {
			_, err := s.Update(t.Context(), func(current *Credential) (*Credential, error) {
				switch action {
				case "login":
					return &Credential{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}, nil
				case "refresh":
					current.RefreshToken = "rotated"
					return current, nil
				default:
					return nil, nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			config, err := shared.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if config.DefaultModel.Auth != "codex" || config.DefaultModel.Model != "openai/gpt-5.4" || config.Providers["openai"].APIKey.Key != "openai-test-key" || config.Providers["anthropic"].APIKey.Key != "anthropic-test-key" || config.Providers["openai"].OAuth["other"].AccessToken != "other" {
				t.Fatal("unrelated configuration changed")
			}
		})
	}
	if _, err := s.Load(t.Context()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatal(err)
	}
	if _, err := NewStore(nil); err == nil {
		t.Fatal("accepted missing store")
	}
}

func TestCredentialStoreFailedUpdate(t *testing.T) {
	s := testStore(t)
	save(t, s, Credential{AccessToken: "old", RefreshToken: "old", ExpiresAt: time.Now().Add(time.Hour)})
	failure := errors.New("update failed")
	_, err := s.Update(t.Context(), func(c *Credential) (*Credential, error) { c.AccessToken = "changed"; return c, failure })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	c, err := s.Load(t.Context())
	if err != nil || c.AccessToken != "old" {
		t.Fatal("failed update persisted", err)
	}
}
