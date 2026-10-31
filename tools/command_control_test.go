package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
)

func TestCommandControlChild(t *testing.T) {
	id := os.Getenv("SOL_TEST_CONTROL_ID")
	if id == "" {
		return
	}
	conn, err := net.DialTimeout("unix", os.Getenv("SOL_COMMAND_CONTROL_SOCKET"), time.Second)
	if err != nil {
		os.Exit(2)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(conn).Encode(map[string]string{"id": id}); err != nil {
		os.Exit(3)
	}
	var ack map[string]bool
	_ = json.NewDecoder(conn).Decode(&ack)
}

func TestBashCommandControlVerifiesBeforeGroupTermination(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			id := "11111111-1111-4111-8111-111111111111"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer host-proof" || r.Method != "POST" || r.URL.Path != "/"+id+"/verify" {
					t.Error("invalid verification request")
					w.WriteHeader(403)
					return
				}
				if valid {
					w.WriteHeader(204)
				} else {
					w.WriteHeader(403)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			tail := filepath.Join(dir, "tail")
			command := fmt.Sprintf("SOL_TEST_CONTROL_ID=%q %q -test.run=^TestCommandControlChild$ || true; ", id, os.Args[0])
			command += "sleep 30; "
			command += fmt.Sprintf("printf tail > %q", tail)
			input, _ := json.Marshal(struct {
				BashInput
				Control CommandControl `json:"_command_control"`
			}{BashInput{Command: command, Description: "Supervisor fixture", Timeout: 10000, Workdir: dir}, CommandControl{VerifyURL: server.URL, Bearer: "host-proof"}})
			pm := bus.NewPermissionManager(bus.New())
			pm.AddRule(bus.PermissionRule{Permission: "*", Pattern: "*", Action: "allow"})
			executor := tool.NewLocalExecutor(tool.Set{"bash": Bash(dir)}, nil)
			started := time.Now()
			result, err := executor.Execute(bus.WithPermissionManager(context.Background(), pm), tool.Request{ToolName: "bash", Input: input})
			if err != nil {
				t.Fatal(err)
			}
			if valid {
				if time.Since(started) > 5*time.Second || result.Metadata["commandSignal"] != id {
					t.Fatal("verified command was not stopped promptly", result.Metadata)
				}
				if _, err := os.Stat(tail); !os.IsNotExist(err) {
					t.Fatal("remaining shell executed")
				}
			} else {
				if result.Metadata["commandSignal"] != nil || result.Metadata["commandControlError"] != "verification_http_403" {
					t.Fatal("unverified signal accepted")
				}
				if _, err := os.Stat(tail); !os.IsNotExist(err) {
					t.Fatal("shell continued after rejected verification", err)
				}
			}
		})
	}
}

func TestBashCommandControlPreservesProcessEnvironment(t *testing.T) {
	t.Setenv("SOL_TEST_INHERITED", "inherited")
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			ctx := context.Background()
			want := "inherited"
			if explicit {
				ctx = WithProcessEnvironment(ctx, []string{"PATH=/usr/bin:/bin"})
				want = "unset"
			}
			pm := bus.NewPermissionManager(bus.New())
			pm.AddRule(bus.PermissionRule{Permission: "*", Pattern: "*", Action: "allow"})
			input, _ := json.Marshal(struct {
				BashInput
				Control CommandControl `json:"_command_control"`
			}{BashInput{Command: `test "${SOL_TEST_INHERITED-unset}" = ` + want, Description: "Environment fixture", Timeout: 1000}, CommandControl{VerifyURL: "http://127.0.0.1:1", Bearer: "host-proof"}})
			executor := tool.NewLocalExecutor(tool.Set{"bash": Bash(t.TempDir())}, nil)
			result, err := executor.Execute(bus.WithPermissionManager(ctx, pm), tool.Request{ToolName: "bash", Input: input})
			if err != nil || result.Metadata["exitCode"] != 0 {
				t.Fatal("supervisor changed process environment", result.Metadata, err)
			}
		})
	}
}

func TestBashCommandControlTransportFailureStopsWithoutApproval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	dir := t.TempDir()
	tail := filepath.Join(dir, "tail")
	command := fmt.Sprintf("SOL_TEST_CONTROL_ID=11111111-1111-4111-8111-111111111111 %q -test.run=^TestCommandControlChild$ || true; sleep 30; printf tail > %q", os.Args[0], tail)
	input, _ := json.Marshal(struct {
		BashInput
		Control CommandControl `json:"_command_control"`
	}{BashInput{Command: command, Description: "Unavailable verifier", Timeout: 10000}, CommandControl{VerifyURL: server.URL, Bearer: "secret-host-proof"}})
	pm := bus.NewPermissionManager(bus.New())
	pm.AddRule(bus.PermissionRule{Permission: "*", Pattern: "*", Action: "allow"})
	executor := tool.NewLocalExecutor(tool.Set{"bash": Bash(dir)}, nil)
	started := time.Now()
	result, err := executor.Execute(bus.WithPermissionManager(t.Context(), pm), tool.Request{ToolName: "bash", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 5*time.Second || result.Metadata["commandControlError"] != "verification_transport" || result.Metadata["commandSignal"] != nil {
		t.Fatalf("transport failure not reported without approval: %+v", result.Metadata)
	}
	if _, err := os.Stat(tail); !os.IsNotExist(err) {
		t.Fatal("shell continued after verifier failure")
	}
}
