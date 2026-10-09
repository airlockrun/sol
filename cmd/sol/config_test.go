package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
)

func TestConfigDefaultCommands(t *testing.T) {
	store, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(t.Context(), func(c *localconfig.Config) error {
		c.Providers["openai"] = localconfig.ProviderAuth{APIKey: &localconfig.APIKeyCredential{Key: "key-secret"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	for _, args := range [][]string{{"model"}, {"model", "--auth", "codex", "openai/gpt-5.4"}, {"model"}, {"model", "anthropic/claude"}, {"model", "--clear"}} {
		if err := runConfig(t.Context(), args, &output, store); err != nil {
			t.Fatal(err)
		}
	}
	c, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultModel != nil || c.Providers["openai"].APIKey.Key != "key-secret" {
		t.Fatal("configuration lost credentials")
	}
	if strings.Contains(output.String(), "secret") || !strings.Contains(output.String(), "auth: codex") || !strings.Contains(output.String(), "auth: api-key") {
		t.Fatal("incorrect or unsafe output")
	}
	for _, args := range [][]string{{"invalid"}, {"model", "--auth", "codex"}, {"model", "--auth", "codex", "anthropic/claude"}, {"model", "--clear", "openai/gpt-4o"}, {"model", "gpt-4o"}, {"model", "--auth", "unknown", "openai/gpt-4o"}, {"model", "one", "two"}} {
		if err := runConfig(t.Context(), args, &output, store); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestDefaultSelectionAndOverrides(t *testing.T) {
	configured := localconfig.Config{Providers: map[string]localconfig.ProviderAuth{}, DefaultModel: &localconfig.ModelSelection{Model: "openai/gpt-5.4", Auth: "codex"}}
	for _, tc := range []struct {
		name, model, auth, wantModel, wantAuth string
		explicitModel, explicitAuth, valid     bool
	}{
		{"stored", "", "", "openai/gpt-5.4", "codex", false, false, true},
		{"explicit same model", "openai/gpt-5.4", "", "openai/gpt-5.4", "api-key", true, false, true},
		{"explicit different provider", "anthropic/claude", "", "anthropic/claude", "api-key", true, false, true},
		{"explicit Codex", "openai/gpt-5.4-mini", "codex", "openai/gpt-5.4-mini", "codex", true, true, true},
		{"override auth", "", "api-key", "openai/gpt-5.4", "api-key", false, true, true},
		{"incompatible override", "anthropic/claude", "codex", "", "", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectModel(configured, tc.model, tc.auth, tc.explicitModel, tc.explicitAuth)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if tc.valid && (got.Model != tc.wantModel || got.Auth != tc.wantAuth) {
				t.Fatal(got)
			}
		})
	}
	got, err := selectModel(localconfig.Config{}, "", "", false, false)
	if err != nil || got.Model != "openai/gpt-4o" || got.Auth != "api-key" {
		t.Fatal(got, err)
	}
	if _, err := selectModel(localconfig.Config{}, "", "codex", false, true); err == nil {
		t.Fatal("selected Codex without a deliberately selected model")
	}
}

func TestAPIKeyPrecedence(t *testing.T) {
	config := localconfig.Config{Providers: map[string]localconfig.ProviderAuth{"openai": {APIKey: &localconfig.APIKeyCredential{Key: "stored-secret"}}}}
	for _, tc := range []struct {
		name, env        string
		present, success bool
		want             string
	}{
		{"stored", "", false, true, "stored-secret"}, {"env wins", "environment-secret", true, true, "environment-secret"}, {"explicit empty disables stored", "", true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := provider.ResolveLocalAPIKey(config, "openai", func(name string) (string, bool) {
				if name != "OPENAI_API_KEY" {
					t.Fatal(name)
				}
				return tc.env, tc.present
			})
			if (err == nil) != tc.success || key != tc.want {
				t.Fatal("incorrect key precedence")
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("credential in error")
			}
		})
	}
	if _, err := provider.ResolveLocalAPIKey(config, "anthropic", func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("missing key accepted")
	}
}

func TestLocalStoreUsesUserConfigDirectory(t *testing.T) {
	// Explicit temporary user config discovery; no real user credentials are read.
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	store, err := localStore()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(context.Background(), func(c *localconfig.Config) error {
		c.DefaultModel = &localconfig.ModelSelection{Model: "openai/gpt-4o", Auth: "api-key"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := localconfig.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "sol", "auth.json") {
		t.Fatalf("unexpected config location: %s", path)
	}
}
