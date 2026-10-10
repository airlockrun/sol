package localconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImportVersionTwo(t *testing.T) {
	s := testStore(t)
	if _, err := s.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	token := OAuthCredential{AccessToken: "import-access", RefreshToken: "import-refresh", ExpiresAt: time.Now().Add(time.Hour), AccountID: "account-routing"}
	credential, _ := json.Marshal(token)
	for _, auth := range []string{"api-key", "codex"} {
		t.Run(auth, func(t *testing.T) {
			store := testStore(t)
			if _, err := store.Load(t.Context()); err != nil {
				t.Fatal(err)
			}
			data := []byte(`{"version":2,"providers":{"openai":{"api_key":{"key":"import-key"},"oauth":{"codex":` + string(credential) + `}},"anthropic":{"api_key":{"key":"other-key"}}},"default_model":{"model":"openai/gpt-5.4","auth":"` + auth + `"}}`)
			source := filepath.Join(filepath.Dir(store.path), "auth.json")
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			imported := c.Providers["codex/openai"].Credentials
			if len(c.Providers) != 3 || c.Providers["default/openai"].Key != "import-key" || imported == nil || imported.AccessToken != token.AccessToken || imported.RefreshToken != token.RefreshToken || imported.AccountID != token.AccountID || !imported.ExpiresAt.Equal(token.ExpiresAt) || c.Providers["default/anthropic"].Key != "other-key" {
				t.Fatal("import lost credentials")
			}
			slug := "default"
			if auth == "codex" {
				slug = "codex"
			}
			if c.DefaultModel != slug+"/openai/gpt-5.4" {
				t.Fatal("wrong imported default")
			}
			preserved, _ := os.ReadFile(source)
			if string(preserved) != string(data) {
				t.Fatal("import mutated source")
			}
			if _, err := store.Update(t.Context(), func(c *Config) error {
				p := c.Providers["codex/openai"]
				p.Credentials = nil
				c.Providers["codex/openai"] = p
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("corrupt inactive backup"), 0600); err != nil {
				t.Fatal(err)
			}
			c, err = store.Load(t.Context())
			if err != nil || c.Providers["codex/openai"].Credentials != nil {
				t.Fatal("inactive credentials reimported", err)
			}
		})
	}
}
func TestImportFailurePreservesSource(t *testing.T) {
	for _, data := range []string{
		`{"version":2,"providers":{"openai":{"oauth":{"other":{}}}}}`,
		`{"version":2,"providers":{},"default_model":{"model":"openai/gpt","auth":"api-key"}}`,
		`{"version":2,"providers":{},"unknown":"secret"}`, `corrupt-secret`,
	} {
		t.Run(data, func(t *testing.T) {
			s := testStore(t)
			if _, err := s.Load(t.Context()); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(filepath.Dir(s.path), "auth.json")
			if err := os.WriteFile(source, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Update(t.Context(), func(*Config) error { t.Fatal("failed import ran mutation"); return nil }); err == nil {
				t.Fatal("unsupported import accepted")
			}
			preserved, _ := os.ReadFile(source)
			if string(preserved) != data {
				t.Fatal("source damaged")
			}
			if _, err := os.Stat(s.path); !os.IsNotExist(err) {
				t.Fatal("failed import persisted config")
			}
		})
	}
}
