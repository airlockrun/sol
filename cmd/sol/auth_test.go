package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/airlockrun/sol/auth/codex"
	"github.com/airlockrun/sol/localconfig"
)

type authTestClient struct {
	fail     error
	deadline bool
}

func (c *authTestClient) StartDeviceAuth(ctx context.Context) (codex.DeviceAuthorization, error) {
	_, c.deadline = ctx.Deadline()
	return codex.DeviceAuthorization{VerificationURL: "https://example.invalid/device", UserCode: "CODE"}, c.fail
}
func (c *authTestClient) CompleteDeviceAuth(context.Context, codex.DeviceAuthorization) (codex.Credential, error) {
	return codex.Credential{AccessToken: "secret-access", RefreshToken: "secret-refresh", ExpiresAt: time.Now().Add(time.Hour)}, c.fail
}

func TestRunAuth(t *testing.T) {
	store, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	client := &authTestClient{}
	var output bytes.Buffer
	if err := runAuth(t.Context(), []string{"login", "work/openai", "--method", "codex"}, &output, client, store, nil); err != nil {
		t.Fatal(err)
	}
	if !client.deadline || !strings.Contains(output.String(), "CODE") {
		t.Fatal("login not bounded or code not displayed")
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("tokens displayed")
	}
	output.Reset()
	if err := runAuth(t.Context(), []string{"status", "work/openai"}, &output, client, store, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("status leaked tokens")
	}
	output.Reset()
	if err := runAuth(t.Context(), []string{"logout", "work/openai"}, &output, client, store, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Local") {
		t.Fatal("logout claims wrong scope")
	}
	if err := runAuth(t.Context(), []string{"status", "work/openai"}, &output, client, store, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunAuthFailures(t *testing.T) {
	store, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"login"}, {"login", "openai"}, {"invalid", "codex"}, {"login", "codex", "extra"}} {
		if err := runAuth(t.Context(), args, &bytes.Buffer{}, &authTestClient{}, store, nil); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	failure := errors.New("login failed")
	if err := runAuth(t.Context(), []string{"login", "work/openai", "--method", "codex"}, &bytes.Buffer{}, &authTestClient{fail: failure}, store, nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	credentials, _ := codex.NewStore(store, "work/openai")
	if _, err := credentials.Load(t.Context()); !errors.Is(err, codex.ErrNotLoggedIn) {
		t.Fatal("failed login persisted", err)
	}
}

func TestProviderAPIKeyCommands(t *testing.T) {
	store, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(t.Context(), func(config *localconfig.Config) error {
		config.Providers["work/openai"] = localconfig.ProviderConfig{Auth: "codex"}
		config.DefaultModel = "work/openai/gpt-5.4"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, _ := codex.NewStore(store, "work/openai")
	_, err = credentials.Update(t.Context(), func(*codex.Credential) (*codex.Credential, error) {
		return &codex.Credential{AccessToken: "oauth-secret", RefreshToken: "refresh-secret", ExpiresAt: time.Now().Add(time.Hour)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	for _, id := range []string{"personal/openai", "personal/anthropic"} {
		secret := func(context.Context, bool) (string, error) { return "key-secret", nil }
		if err := runAuth(t.Context(), []string{"set-key", id, "--stdin"}, &output, nil, store, secret); err != nil {
			t.Fatal(err)
		}
		if err := runAuth(t.Context(), []string{"status", id}, &output, nil, store, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := runAuth(t.Context(), []string{"remove-key", "personal/openai"}, &output, nil, store, nil); err != nil {
		t.Fatal(err)
	}
	config, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if config.Providers["personal/openai"].Key != "" || config.Providers["personal/anthropic"].Key == "" || config.Providers["work/openai"].Credentials.AccessToken != "oauth-secret" || config.DefaultModel != "work/openai/gpt-5.4" {
		t.Fatal("key commands changed unrelated settings")
	}
	if err := runAuth(t.Context(), []string{"logout", "work/openai"}, &output, nil, store, nil); err != nil {
		t.Fatal(err)
	}
	config, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if config.Providers["personal/anthropic"].Key == "" || config.DefaultModel != "work/openai/gpt-5.4" {
		t.Fatal("logout lost other configuration")
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatal("secret output")
	}
}

func TestAPIKeyArgumentsDoNotAcceptSecrets(t *testing.T) {
	store, err := localconfig.NewFileStore(filepath.Join(t.TempDir(), "sol", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"set-key", "work/openai", "argument-secret"}, {"set-key", "work/openai", "--stdin", "argument-secret"}, {"set-key", "codex"}, {"remove-key", "codex"}} {
		var output bytes.Buffer
		err := runAuth(t.Context(), args, &output, nil, store, func(context.Context, bool) (string, error) { t.Fatal("invalid command read a secret"); return "", nil })
		if err == nil || strings.Contains(err.Error()+output.String(), "argument-secret") {
			t.Fatal("accepted or exposed secret argument", err)
		}
	}
}
