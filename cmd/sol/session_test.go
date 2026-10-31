package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/airlockrun/sol/localconfig"
)

func TestBatchSessionResumeCompactAndIsolation(t *testing.T) {
	root := t.TempDir()
	store, _ := localconfig.NewFileStore(filepath.Join(root, "sol", "config.json"))
	_, err := store.Update(t.Context(), func(c *localconfig.Config) error {
		c.Providers["test/openai"] = localconfig.ProviderConfig{Auth: "api-key", Key: "fixture-key"}
		c.DefaultModel, c.DefaultAgent = "test/openai/gpt-4o-mini", "plan"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture-answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	}))
	defer server.Close()
	run := func(args []string, selector, depth string) (string, string, error) {
		t.Helper()
		raw, _ := json.Marshal(args)
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIAccountAndProxyIsolation$")
		for _, e := range os.Environ() {
			key, _, _ := strings.Cut(e, "=")
			switch key {
			case "HOME", "APPDATA", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "SOL_SESSION", "SOL_DEPTH", "SOL_CLI_TEST_CHILD", "SOL_CLI_TEST_ARGS", "AIRLOCK_API_URL", "AIRLOCK_BUILD_TOKEN", "OPENAI_BASE_URL":
				continue
			}
			cmd.Env = append(cmd.Env, e)
		}
		cmd.Env = append(cmd.Env, "HOME="+root, "APPDATA="+root, "XDG_CONFIG_HOME="+root, "XDG_STATE_HOME="+root, "SOL_CLI_TEST_CHILD=1", "SOL_CLI_TEST_ARGS="+string(raw), "OPENAI_BASE_URL="+server.URL)
		if selector != "" {
			cmd.Env = append(cmd.Env, "SOL_SESSION="+selector)
		}
		if depth != "" {
			cmd.Env = append(cmd.Env, "SOL_DEPTH="+depth)
		}
		cmd.Dir = root
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err := cmd.Run()
		return out.String(), diagnostic.String(), err
	}
	id, diagnostic, err := run([]string{"--session", "new"}, "", "")
	if err != nil || diagnostic != "" {
		t.Fatalf("new: %v %s", err, diagnostic)
	}
	id = strings.TrimSpace(id)
	for _, prompt := range []string{"first-request", "second-request"} {
		out, diagnostic, err := run([]string{prompt}, id, "")
		if err != nil || out != "fixture-answer\n" || diagnostic != "" {
			t.Fatalf("batch: %v stdout=%q stderr=%q", err, out, diagnostic)
		}
	}
	out, diagnostic, err := run([]string{"--session", "compact"}, id, "")
	if err != nil || !strings.Contains(out, "Compacted ") {
		t.Fatalf("compact: %v %s %s", err, out, diagnostic)
	}
	_, _, err = run([]string{"third-request"}, id, "")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(requests) != 4 || !strings.Contains(requests[1], "first-request") || !strings.Contains(requests[1], "second-request") {
		t.Fatal("resume or request count incorrect", requests)
	}
	mu.Unlock()
	for i, args := range [][]string{
		{"--sid", id, "--compact"},
		{"--session", "compact", id},
		{"--sid", id, "--session", "compact"},
	} {
		out, diagnostic, err := run(args, "missing", "")
		if i > 0 {
			if err == nil || !strings.Contains(diagnostic, "no older history") {
				t.Fatalf("repeated compact %q: %v %q %q", args, err, out, diagnostic)
			}
			continue
		}
		if err != nil || !strings.Contains(out, "Compacted "+id) || diagnostic != "" {
			t.Fatalf("explicit compact %q: %v %q %q", args, err, out, diagnostic)
		}
	}
	state, err := localconfig.OpenSession(t.Context(), filepath.Join(root, "sol", "sessions"), id, false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Record.Agent != "plan" || len(state.Record.History) != 6 || len(state.Record.Context) >= len(state.Record.History) {
		t.Fatalf("unexpected persisted state: %+v", state.Record)
	}
	state.Close()
	// Explicit selection overrides a different terminal selection.
	_, _, err = run([]string{"--sid", id, "override-request"}, "missing", "1")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before := len(requests)
	mu.Unlock()
	for _, depth := range []string{"2", "-1", "invalid"} {
		_, diagnostic, err = run([]string{"blocked-request"}, id, depth)
		if err == nil || diagnostic == "" {
			t.Fatal("nested guard accepted", depth)
		}
	}
	mu.Lock()
	if len(requests) != before {
		t.Fatal("guard reached the model")
	}
	mu.Unlock()
	for _, args := range [][]string{
		{"session", "list"}, {"auth", "status"}, {"config", "agent", "plan"},
		{"clear"}, {"compact"}, {"request", "--session", "list", "--"},
		{"--", "--help"}, {"--", "--compact"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			mu.Lock()
			before := len(requests)
			mu.Unlock()
			out, diagnostic, err := run(args, id, "")
			if err != nil || out != "fixture-answer\n" || diagnostic != "" {
				t.Fatalf("literal request: %v %q %q", err, out, diagnostic)
			}
			want := strings.Join(args, " ")
			if args[0] == "--" {
				want = strings.Join(args[1:], " ")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != before+1 || !strings.Contains(requests[before], want) {
				t.Fatal("literal prompt did not reach model exactly once", requests)
			}
		})
	}
	state, err = localconfig.OpenSession(t.Context(), filepath.Join(root, "sol", "sessions"), id, false)
	if err != nil {
		t.Fatal(err)
	}
	retained := state.Record
	state.Close()
	for _, args := range [][]string{{"--clear"}, {"--sid", id, "--clear"}} {
		out, diagnostic, err := run(args, id, "2")
		if err != nil || out != "Cleared context for session "+id+"; history retained.\n" || diagnostic != "" {
			t.Fatalf("clear: %v %q %q", err, out, diagnostic)
		}
	}
	out, diagnostic, err = run([]string{"fresh-context-request"}, id, "")
	if err != nil || out != "fixture-answer\n" || diagnostic != "" {
		t.Fatalf("fresh turn: %v %q %q", err, out, diagnostic)
	}
	mu.Lock()
	last := requests[len(requests)-1]
	mu.Unlock()
	if strings.Contains(last, "first-request") || strings.Contains(last, "fixture-answer") || !strings.Contains(last, "fresh-context-request") {
		t.Fatal("old context fed to model after clear", last)
	}
	state, err = localconfig.OpenSession(t.Context(), filepath.Join(root, "sol", "sessions"), id, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Record.History) != len(retained.History)+2 || !reflect.DeepEqual(state.Record.History[:len(retained.History)], retained.History) || len(state.Record.Context) != 2 {
		t.Fatal("clear or next turn altered full transcript", state.Record)
	}
	// Storage-only clear works with an invalid model, a different working directory,
	// an interruption marker and a corrupt credential configuration.
	state.Record.Model, state.Record.WorkDir, state.Record.Active = "invalid", filepath.Join(root, "elsewhere"), true
	if err := state.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	state.Close()
	configPath := filepath.Join(root, "sol", "config.json")
	if err := os.WriteFile(configPath, []byte("invalid-config"), 0600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before = len(requests)
	mu.Unlock()
	for _, args := range [][]string{{"--sid", id, "--clear"}, {"--help"}, {"--version"}, {"--session", "--help"}, {"--auth", "--help"}, {"--config", "--help"}, {"--session", "list"}, {"--session", "show", id}} {
		if _, diagnostic, err := run(args, "missing", "2"); err != nil || diagnostic != "" {
			t.Fatalf("offline administration %q: %v %s", args, err, diagnostic)
		}
	}
	for _, args := range [][]string{
		{"--sid", id, "--session", "compact", id}, {"--sid", id, "--session", "new"},
		{"--sid", id, "--session", "list"}, {"--compact", "--clear"}, {"--clear", "request"},
		{"--unknown"}, {"-session", id, "request"}, {"--"}, {"--compact"}, {"--session", "compact", id},
	} {
		if _, diagnostic, err := run(args, id, "2"); err == nil || diagnostic == "" {
			t.Fatalf("accepted invalid/blocked command %q", args)
		}
	}
	mu.Lock()
	if len(requests) != before {
		t.Fatal("offline command reached model")
	}
	mu.Unlock()
	configBytes, err := os.ReadFile(configPath)
	if err != nil || string(configBytes) != "invalid-config" {
		t.Fatal("offline commands altered private config", err)
	}
	state, err = localconfig.OpenSession(t.Context(), filepath.Join(root, "sol", "sessions"), id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if state.Record.Active || len(state.Record.Context) != 0 || state.Record.Model != "invalid" || state.Record.WorkDir != filepath.Join(root, "elsewhere") {
		t.Fatal("offline clear changed metadata or failed to reset context", state.Record)
	}
	if _, diagnostic, err := run([]string{"--sid", id, "--clear"}, "", ""); err == nil || !strings.Contains(diagnostic, "busy") {
		t.Fatal("clear bypassed session lease", err, diagnostic)
	}
}

func TestChildEnvironment(t *testing.T) {
	t.Setenv("SOL_SESSION", "parent")
	t.Setenv("SOL_DEPTH", "1")
	for _, e := range childEnvironment(1) {
		if strings.HasPrefix(e, "SOL_SESSION=") {
			t.Fatal("parent session leaked to child")
		}
		if strings.HasPrefix(e, "SOL_DEPTH=") && e != "SOL_DEPTH=2" {
			t.Fatal("depth not advanced")
		}
	}
}
