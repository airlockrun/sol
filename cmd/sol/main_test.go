package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol"
	"github.com/airlockrun/sol/auth/codex"
	"github.com/airlockrun/sol/localconfig"
	"github.com/airlockrun/sol/provider"
)

func TestValidateAuthMode(t *testing.T) {
	for _, tc := range []struct {
		name, auth, provider, model, proxy, base string
		explicit, valid                          bool
	}{
		{"API key", "api-key", "openai", "gpt-4o", "", "", false, true},
		{"Codex", "codex", "openai", "gpt-5.4", "", "", true, true},
		{"missing explicit model", "codex", "openai", "gpt-4o", "", "", false, false},
		{"wrong provider", "codex", "anthropic", "claude", "", "", true, false},
		{"missing model", "codex", "openai", "", "", "", true, false},
		{"proxy", "codex", "openai", "gpt-5.4", "http://proxy", "", true, false},
		{"base URL", "codex", "openai", "gpt-5.4", "", "http://base", true, false},
		{"unknown auth", "auto", "openai", "gpt-5.4", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAuthMode(tc.auth, tc.provider, tc.model, tc.proxy, tc.base, tc.explicit)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestCLIConfiguredKeyAndDefault(t *testing.T) {
	if os.Getenv("SOL_CLI_CONFIG_TEST") == "1" {
		flag.CommandLine = flag.NewFlagSet("sol", flag.ExitOnError)
		os.Args = []string{"sol", "-notitle", "-agent", "plan", "Reply with cli-ok."}
		main()
		return
	}
	root := t.TempDir()
	configRoot := root
	if runtime.GOOS == "darwin" {
		configRoot = filepath.Join(root, "Library", "Application Support")
	}
	store, err := localconfig.NewFileStore(filepath.Join(configRoot, "sol", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(t.Context(), func(c *localconfig.Config) error {
		c.DefaultModel = &localconfig.ModelSelection{Model: "openai/gpt-4o-mini", Auth: "api-key"}
		c.Providers["openai"] = localconfig.ProviderAuth{APIKey: &localconfig.APIKeyCredential{Key: "stored-test-secret"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("OPENAI_API_KEY=project-test-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, value, want string
		present, success  bool
	}{
		{"stored", "", "stored-test-secret", false, true}, {"environment", "environment-test-secret", "environment-test-secret", true, true}, {"explicit empty", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+tc.want {
					t.Error("CLI did not use expected explicit credential")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "gpt-4o-mini" {
					t.Error("CLI did not select saved model")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"cli-ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			}))
			defer server.Close()
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIConfiguredKeyAndDefault$")
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				switch name {
				case "SOL_CLI_CONFIG_TEST", "HOME", "APPDATA", "XDG_CONFIG_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL", "AIRLOCK_API_URL", "AIRLOCK_BUILD_TOKEN":
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "SOL_CLI_CONFIG_TEST=1", "HOME="+root, "APPDATA="+root, "XDG_CONFIG_HOME="+root, "OPENAI_BASE_URL="+server.URL, "AIRLOCK_API_URL=", "AIRLOCK_BUILD_TOKEN=")
			if tc.present {
				cmd.Env = append(cmd.Env, "OPENAI_API_KEY="+tc.value)
			}
			cmd.Dir = project
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("unexpected CLI exit: %v\n%s", err, output)
			}
			if tc.success && (calls.Load() != 1 || !strings.Contains(string(output), "cli-ok") || !strings.Contains(string(output), "auth: api-key")) {
				t.Fatal("CLI did not execute configured model")
			}
			if !tc.success && calls.Load() != 0 {
				t.Fatal("empty env reached network")
			}
			for _, secret := range []string{"stored-test-secret", "environment-test-secret", "project-test-secret"} {
				if strings.Contains(string(output), secret) {
					t.Fatal("CLI exposed credential")
				}
			}
		})
	}
}

type cliTestSource struct{}

func (cliTestSource) Access(context.Context) (codex.Access, error) {
	return codex.Access{Token: "explicit-test-token"}, nil
}

func TestCodexTitleSelection(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			want := "gpt-5.4"
			if explicit {
				want = "gpt-5.4-mini"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["model"] != want || body["instructions"] == nil || body["max_output_tokens"] != nil || r.Header.Get("Authorization") != "Bearer explicit-test-token" {
					t.Errorf("incorrect title request: %v", body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Title\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"title\"}}\n\n")
			}))
			defer server.Close()
			opts := provider.CodexOptions{Credentials: cliTestSource{}, HTTPClient: server.Client(), URL: server.URL, UserAgent: "sol/test", SessionID: "test"}
			model, title, err := codexModels("gpt-5.4", "openai/gpt-5.4-mini", explicit, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !explicit && model != title {
				t.Fatal("default title did not share model")
			}
			result := <-sol.GenerateTitleWithModelAsync(t.Context(), "prompt", title)
			if result.Error != nil || result.Title != "Title" {
				t.Fatal(result)
			}
			if _, _, err := codexModels("gpt-5.4", "anthropic/claude", true, opts); err == nil {
				t.Fatal("accepted incompatible title model")
			}
		})
	}
}

func TestCodexSearchRequiresIndependentCredentials(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("PERPLEXITY_API_KEY", "")
	if _, ok := resolveSearch("codex", "openai", "must-not-use", localconfig.Config{}); ok {
		t.Fatal("Codex reused OpenAI search credentials")
	}
	t.Setenv("BRAVE_API_KEY", "explicit-search-test-key")
	if _, ok := resolveSearch("codex", "openai", "must-not-use", localconfig.Config{}); !ok {
		t.Fatal("dedicated search unavailable")
	}
	t.Setenv("BRAVE_API_KEY", "")
	config := localconfig.Config{Providers: map[string]localconfig.ProviderAuth{"brave": {APIKey: &localconfig.APIKeyCredential{Key: "stored-search-test-key"}}}}
	if _, ok := resolveSearch("codex", "openai", "must-not-use", config); ok {
		t.Fatal("explicit empty search env did not disable stored key")
	}
	if err := os.Unsetenv("BRAVE_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if _, ok := resolveSearch("codex", "openai", "must-not-use", config); !ok {
		t.Fatal("stored independent search key unavailable")
	}
	t.Setenv("PERPLEXITY_API_KEY", "independent-test-key")
	if _, ok := resolveSearch("codex", "openai", "must-not-use", config); !ok {
		t.Fatal("independent search unavailable")
	}
}

func TestCLIProxySkipsLocalConfiguration(t *testing.T) {
	if os.Getenv("SOL_CLI_PROXY_TEST") == "1" {
		flag.CommandLine = flag.NewFlagSet("sol", flag.ExitOnError)
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv("SOL_CLI_PROXY_ARGS")), &args); err != nil {
			t.Fatal(err)
		}
		os.Args = append([]string{"sol"}, args...)
		main()
		return
	}
	for _, tc := range []struct {
		name, fixture, model, auth, want string
		success                          bool
	}{
		{"absent store", "absent", "", "", "openai/gpt-4o", true},
		{"bad store", "invalid", "", "", "openai/gpt-4o", true},
		{"saved Codex ignored", "codex", "", "", "openai/gpt-4o", true},
		{"explicit model", "invalid", "openai/gpt-4o-mini", "api-key", "openai/gpt-4o-mini", true},
		{"explicit Codex rejected", "absent", "openai/gpt-5.4", "codex", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			configRoot := root
			if runtime.GOOS == "darwin" {
				configRoot = filepath.Join(root, "Library", "Application Support")
			}
			dir := filepath.Join(configRoot, "sol")
			path := filepath.Join(dir, "auth.json")
			if tc.fixture == "invalid" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("unreadable configuration JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fixture == "codex" {
				store, err := localconfig.NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = store.Update(t.Context(), func(config *localconfig.Config) error {
					config.DefaultModel = &localconfig.ModelSelection{Model: "openai/gpt-5.4", Auth: "codex"}
					config.Providers["openai"] = localconfig.ProviderAuth{OAuth: map[string]localconfig.OAuthCredential{"codex": {AccessToken: "local-access-test", RefreshToken: "local-refresh-test", ExpiresAt: time.Now().Add(time.Hour)}}}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/api/agent/llm/stream" || r.Header.Get("Authorization") != "Bearer proxy-test-token" {
					t.Error("incorrect proxy request")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model_id"] != tc.want {
					t.Error("proxy inherited local model default")
				}
				encoder := json.NewEncoder(w)
				encoder.Encode(stream.Event{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "proxy-ok"}})
				encoder.Encode(stream.Event{Type: stream.EventFinish, Data: stream.FinishEvent{FinishReason: stream.FinishReasonStop, Usage: stream.UsageFrom(1, 1)}})
			}))
			defer server.Close()
			args := []string{"-notitle", "-agent", "plan"}
			if tc.model != "" {
				args = append(args, "-model", tc.model)
			}
			if tc.auth != "" {
				args = append(args, "-auth", tc.auth)
			}
			args = append(args, "Reply with proxy-ok.")
			encoded, _ := json.Marshal(args)
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIProxySkipsLocalConfiguration$")
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				switch name {
				case "SOL_CLI_PROXY_TEST", "SOL_CLI_PROXY_ARGS", "HOME", "APPDATA", "XDG_CONFIG_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL", "AIRLOCK_API_URL", "AIRLOCK_BUILD_TOKEN":
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "SOL_CLI_PROXY_TEST=1", "SOL_CLI_PROXY_ARGS="+string(encoded), "HOME="+root, "APPDATA="+root, "XDG_CONFIG_HOME="+root, "AIRLOCK_API_URL="+server.URL, "AIRLOCK_BUILD_TOKEN=proxy-test-token", "OPENAI_API_KEY=", "OPENAI_BASE_URL=")
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("unexpected proxy exit: %v\n%s", err, output)
			}
			if tc.success && (calls.Load() != 1 || !strings.Contains(string(output), "proxy-ok")) {
				t.Fatal("proxy request did not complete")
			}
			if !tc.success && (calls.Load() != 0 || !strings.Contains(string(output), "direct local mode")) {
				t.Fatal("explicit Codex did not fail before proxy/config access")
			}
			if tc.fixture == "absent" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("proxy created local configuration directory")
				}
			} else {
				after, err := os.ReadFile(path)
				if err != nil || string(before) != string(after) {
					t.Fatal("proxy changed local credentials")
				}
				if tc.fixture == "invalid" {
					if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
						t.Fatal("proxy attempted local store load")
					}
				}
			}
			for _, secret := range []string{"local-access-test", "local-refresh-test", "proxy-test-token"} {
				if strings.Contains(string(output), secret) {
					t.Fatal("proxy exposed a credential")
				}
			}
		})
	}
}
