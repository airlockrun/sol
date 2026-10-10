package main

import (
	"bytes"
	"github.com/airlockrun/sol/localconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cliStore(t *testing.T) *localconfig.FileStore {
	t.Helper()
	s, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestConfigDefaultCommands(t *testing.T) {
	store := cliStore(t)
	if _, err := store.Update(t.Context(), func(c *localconfig.Config) error {
		c.Providers["work/openai"] = localconfig.ProviderConfig{Auth: "codex"}
		c.Providers["personal/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "key-secret"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	for _, args := range [][]string{{"model"}, {"model", "work/openai/gpt-5.4"}, {"model"}, {"model", "personal/openai/gpt-4o"}, {"model", "--clear"}} {
		if err := runConfig(t.Context(), args, &output, store); err != nil {
			t.Fatal(err)
		}
	}
	c, err := store.Load(t.Context())
	if err != nil || c.DefaultModel != "" || c.Providers["personal/openai"].Key != "key-secret" {
		t.Fatal("lost configuration", err)
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("secret output")
	}
	for _, args := range [][]string{{"model", "--auth", "codex"}, {"model", "missing/openai/gpt-4o"}, {"model", "work/fireworks/gpt"}, {"model", "openai/gpt-4o"}, {"model", "work/openai/unknown-model"}, {"model", "--clear", "extra"}} {
		if err := runConfig(t.Context(), args, &output, store); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestDefaultSelectionAndOverrides(t *testing.T) {
	c := localconfig.Config{DefaultModel: "work/openai/gpt-5.4", Providers: map[string]localconfig.ProviderConfig{"work/openai": {Auth: "codex"}, "personal/openai": {Auth: "api-key", Key: "secret"}}}
	for _, tc := range []struct{ name, model, entry, auth string }{{"default", "", "work/openai", "codex"}, {"override", "personal/openai/gpt-5.4", "personal/openai", "api-key"}} {
		t.Run(tc.name, func(t *testing.T) {
			ref, p, err := selectModel(c, tc.model)
			if err != nil || ref.Entry != tc.entry || p.Auth != tc.auth {
				t.Fatal("wrong account selection", err)
			}
		})
	}
	if _, _, err := selectModel(localconfig.Config{}, ""); err == nil {
		t.Fatal("implicit default selected")
	}
	if _, _, err := selectModel(c, "other/openai/gpt-5.4"); err == nil {
		t.Fatal("unknown account accepted")
	}
}

func TestAgentConfigPreservesProviderAndModel(t *testing.T) {
	store := cliStore(t)
	_, err := store.Update(t.Context(), func(c *localconfig.Config) error {
		c.Providers["test/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "private-key"}
		c.DefaultModel = "test/openai/gpt-4o-mini"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runConfig(t.Context(), []string{"agent", "plan"}, &output, store); err != nil {
		t.Fatal(err)
	}
	c, err := store.Load(t.Context())
	if err != nil || c.DefaultAgent != "plan" || c.DefaultModel != "test/openai/gpt-4o-mini" || c.Providers["test/openai"].Key != "private-key" {
		t.Fatal("configuration not preserved", err)
	}
	if err := runConfig(t.Context(), []string{"agent", "unknown"}, &output, store); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if err := runConfig(t.Context(), []string{"agent", "--clear"}, &output, store); err != nil {
		t.Fatal(err)
	}
	c, err = store.Load(t.Context())
	if err != nil || c.DefaultAgent != "" {
		t.Fatal("default agent not cleared", err)
	}
}
func TestLocalStoreUsesUserConfigDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	store, err := localStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	path, err := localconfig.DefaultPath()
	dir, dirErr := os.UserConfigDir()
	if err != nil || dirErr != nil || path != filepath.Join(dir, "sol", "config.json") {
		t.Fatal("incorrect config location")
	}
}
