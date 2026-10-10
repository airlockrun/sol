package codex

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/airlockrun/sol/localconfig"
)

func TestCredentialStorePreservesSharedConfiguration(t *testing.T) {
	shared, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = shared.Update(t.Context(), func(config *localconfig.Config) error {
		config.DefaultModel = "work/openai/gpt-5.4"
		config.Providers["work/openai"] = localconfig.ProviderConfig{Auth: "codex"}
		config.Providers["personal/openai"] = localconfig.ProviderConfig{Auth: "codex", Credentials: &Credential{AccessToken: "other", RefreshToken: "other", ExpiresAt: time.Now().Add(time.Hour)}}
		config.Providers["default/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "openai-test-key"}
		config.Providers["default/anthropic"] = localconfig.ProviderConfig{Auth: "api-key", Key: "anthropic-test-key"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(shared, "work/openai")
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
			if config.DefaultModel != "work/openai/gpt-5.4" || config.Providers["default/openai"].Key != "openai-test-key" || config.Providers["default/anthropic"].Key != "anthropic-test-key" || config.Providers["personal/openai"].Credentials.AccessToken != "other" {
				t.Fatal("unrelated configuration changed")
			}
		})
	}
	if _, err := s.Load(t.Context()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatal(err)
	}
	if _, err := NewStore(nil, "work/openai"); err == nil {
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

func TestCredentialStoreDoesNotResurrectChangedAccount(t *testing.T) {
	s := testStore(t)
	save(t, s, Credential{AccessToken: "old", RefreshToken: "old", ExpiresAt: time.Now().Add(-time.Hour)})
	_, err := s.UpdateRefresh(t.Context(), func(*Credential) (*Credential, error) {
		// A direct file transaction models an editor changing the entry mid-exchange.
		_, err := s.store.Update(t.Context(), func(c *localconfig.Config) error {
			c.Providers[s.entry] = localconfig.ProviderConfig{Auth: "codex"}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return &Credential{AccessToken: "new", RefreshToken: "rotated", ExpiresAt: time.Now().Add(time.Hour)}, nil
	})
	if err == nil {
		t.Fatal("changed binding accepted")
	}
	if _, err := s.Load(t.Context()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatal("logout resurrected", err)
	}
}
