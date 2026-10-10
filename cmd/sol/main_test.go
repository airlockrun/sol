package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol/localconfig"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCLIAccountAndProxyIsolation(t *testing.T) {
	if os.Getenv("SOL_CLI_TEST_CHILD") == "1" {
		flag.CommandLine = flag.NewFlagSet("sol", flag.ExitOnError)
		var args []string
		if err := json.Unmarshal([]byte(os.Getenv("SOL_CLI_TEST_ARGS")), &args); err != nil {
			t.Fatal(err)
		}
		os.Args = append([]string{"sol"}, args...)
		main()
		os.Exit(0)
	}
	for _, tc := range []struct {
		name, model    string
		proxy, success bool
	}{
		{"default", "", false, true}, {"explicit account", "personal/openai/gpt-4o-mini", false, true}, {"unknown account", "missing/openai/gpt-4o-mini", false, false}, {"short reference", "openai/gpt-4o-mini", false, false}, {"proxy ignores malformed config", "openai/gpt-4o-mini", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			configRoot := root
			if runtime.GOOS == "darwin" {
				configRoot = filepath.Join(root, "Library", "Application Support")
			}
			path := filepath.Join(configRoot, "sol", "config.json")
			store, _ := localconfig.NewFileStore(path)
			_, err := store.Update(t.Context(), func(c *localconfig.Config) error {
				c.Providers["work/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "work-test-secret"}
				c.Providers["personal/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "personal-test-secret"}
				c.DefaultModel = "work/openai/gpt-4o-mini"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.proxy {
				if err := os.WriteFile(path, []byte("invalid-config"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				want := "work-test-secret"
				if tc.model == "personal/openai/gpt-4o-mini" {
					want = "personal-test-secret"
				}
				if tc.proxy {
					want = "proxy-test-secret"
				}
				if r.Header.Get("Authorization") != "Bearer "+want {
					t.Error("wrong account credentials")
				}
				if tc.proxy {
					encoder := json.NewEncoder(w)
					encoder.Encode(stream.Event{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "cli-ok"}})
					encoder.Encode(stream.Event{Type: stream.EventFinish, Data: stream.FinishEvent{FinishReason: stream.FinishReasonStop}})
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"cli-ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
				}
			}))
			defer server.Close()
			args := []string{"-agent", "plan"}
			if tc.model != "" {
				args = append(args, "-model", tc.model)
			}
			args = append(args, "Reply cli-ok")
			raw, _ := json.Marshal(args)
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIAccountAndProxyIsolation$")
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				switch name {
				case "SOL_CLI_TEST_CHILD", "SOL_CLI_TEST_ARGS", "SOL_SESSION", "SOL_DEPTH", "XDG_STATE_HOME", "HOME", "APPDATA", "XDG_CONFIG_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL", "AIRLOCK_API_URL", "AIRLOCK_BUILD_TOKEN":
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			proxy := ""
			if tc.proxy {
				proxy = server.URL
			}
			cmd.Env = append(cmd.Env, "SOL_CLI_TEST_CHILD=1", "SOL_CLI_TEST_ARGS="+string(raw), "HOME="+root, "APPDATA="+root, "XDG_CONFIG_HOME="+root, "OPENAI_API_KEY=must-not-use", "OPENAI_BASE_URL="+server.URL, "AIRLOCK_API_URL="+proxy, "AIRLOCK_BUILD_TOKEN=proxy-test-secret")
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("CLI exit %v: %s", err, output)
			}
			if tc.success && (calls.Load() != 1 || !strings.Contains(string(output), "cli-ok")) {
				t.Fatal("CLI did not execute")
			}
			if !tc.success && calls.Load() != 0 {
				t.Fatal("invalid reference reached network")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("CLI changed configuration")
			}
			for _, secret := range []string{"work-test-secret", "personal-test-secret", "proxy-test-secret", "must-not-use"} {
				if strings.Contains(string(output), secret) {
					t.Fatal("secret output")
				}
			}
		})
	}
}
