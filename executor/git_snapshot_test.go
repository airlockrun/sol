package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/airlockrun/goai/tool"
)

func snapshotTestGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = path
	cmd.Env = snapshotGitEnv()
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestGitSnapshotIgnoresInheritedRepository(t *testing.T) {
	foreign := t.TempDir()
	snapshotTestGit(t, foreign, "init", "-b", "main")
	snapshotTestGit(t, foreign, "config", "user.name", "Foreign Fixture")
	snapshotTestGit(t, foreign, "config", "user.email", "foreign@example.invalid")
	if err := os.WriteFile(filepath.Join(foreign, "retained.txt"), []byte("retained\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshotTestGit(t, foreign, "add", "-A")
	snapshotTestGit(t, foreign, "commit", "-m", "foreign")
	gitDir := filepath.Join(foreign, ".git")
	before := map[string][]byte{}
	for _, name := range []string{"HEAD", "index", "config", "refs/heads/main"} {
		data, err := os.ReadFile(filepath.Join(gitDir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\nname = Snapshot Fixture\nemail = snapshot@example.invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	for key, value := range map[string]string{
		"GIT_DIR": gitDir, "GIT_WORK_TREE": foreign, "GIT_COMMON_DIR": gitDir,
		"GIT_INDEX_FILE": filepath.Join(gitDir, "index"), "GIT_OBJECT_DIRECTORY": filepath.Join(gitDir, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(gitDir, "objects"),
		"GIT_CONFIG":                       filepath.Join(gitDir, "config"), "GIT_CONFIG_PARAMETERS": "'user.name=Injected'",
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.worktree", "GIT_CONFIG_VALUE_0": foreign,
		"GIT_IMPLICIT_WORK_TREE": "0", "GIT_GRAFT_FILE": filepath.Join(gitDir, "grafts"),
		"GIT_NO_REPLACE_OBJECTS": "1", "GIT_REPLACE_REF_BASE": "refs/foreign/", "GIT_PREFIX": "foreign/",
		"GIT_SHALLOW_FILE": filepath.Join(gitDir, "shallow"),
	} {
		t.Setenv(key, value)
	}
	workspace := t.TempDir()
	snapshotTestGit(t, workspace, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("snapshot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executor := NewGitSnapshotExecutor(tool.NewLocalExecutor(tool.Set{}, nil), workspace, t.TempDir())
	response, err := executor.Execute(t.Context(), tool.Request{ToolName: GitSnapshotTool})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := DecodeGitSnapshot(response)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Head != snapshotTestGit(t, workspace, "rev-parse", "HEAD") || snapshotTestGit(t, workspace, "show", "HEAD:source.txt") != "snapshot" {
		t.Fatal("capture did not commit the independent workspace")
	}
	if got := snapshotTestGit(t, workspace, "config", "user.name"); got != "Snapshot Fixture" {
		t.Fatalf("global identity not preserved: %s", got)
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(gitDir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("foreign repository %s changed: %v", name, err)
		}
	}
}

func TestSnapshotGitEnvironmentRemovesRepositoryCoordinates(t *testing.T) {
	keys := strings.Fields(snapshotTestGit(t, t.TempDir(), "rev-parse", "--local-env-vars"))
	keys = append(keys, "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0")
	for _, key := range keys {
		t.Setenv(key, "foreign-coordinate")
	}
	for _, entry := range snapshotGitEnv() {
		key, _, _ := strings.Cut(entry, "=")
		for _, forbidden := range keys {
			if key == forbidden {
				t.Fatalf("Git repository-local environment retained: %s", key)
			}
		}
	}
}

func TestSnapshotGitEnvironmentPreservesTransport(t *testing.T) {
	for _, key := range []string{"PATH", "HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_SSH_COMMAND", "GIT_ASKPASS"} {
		t.Setenv(key, "preserved-"+key)
	}
	got := map[string]string{}
	for _, entry := range snapshotGitEnv() {
		key, value, _ := strings.Cut(entry, "=")
		got[key] = value
	}
	for _, key := range []string{"PATH", "HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_SSH_COMMAND", "GIT_ASKPASS"} {
		if got[key] != "preserved-"+key {
			t.Fatalf("transport/global setting %s lost", key)
		}
	}
}

func TestGitSnapshotCapturesChangedSourceIntoImmutableVerifiedBundle(t *testing.T) {
	workspace := t.TempDir()
	artifacts := t.TempDir()
	snapshotTestGit(t, workspace, "init", "-b", "main")
	snapshotTestGit(t, workspace, "config", "user.name", "Fixture")
	snapshotTestGit(t, workspace, "config", "user.email", "fixture@example.invalid")
	path := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshotTestGit(t, workspace, "add", "-A")
	snapshotTestGit(t, workspace, "commit", "-m", "base")
	if err := os.WriteFile(path, []byte("first capture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executor := NewGitSnapshotExecutor(tool.NewLocalExecutor(tool.Set{}, nil), workspace, artifacts)
	response, err := executor.Execute(t.Context(), tool.Request{ToolName: GitSnapshotTool})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := DecodeGitSnapshot(response)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/snapshots/"+snapshot.ID, nil)
	recorder := httptest.NewRecorder()
	executor.ArtifactHandler().ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatal(recorder.Code)
	}
	hash := sha256.Sum256(recorder.Body.Bytes())
	if hex.EncodeToString(hash[:]) != snapshot.SHA256 || int64(recorder.Body.Len()) != snapshot.Size {
		t.Fatal("capture descriptor does not authenticate exact bytes")
	}
	if err := os.WriteFile(path, []byte("later uncheckpointed source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "source.bundle")
	if err := os.WriteFile(bundle, recorder.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "import")
	snapshotTestGit(t, workspace, "clone", bundle, clone)
	if got := snapshotTestGit(t, clone, "rev-parse", "HEAD"); got != snapshot.Head {
		t.Fatal("immutable capture head changed", got, snapshot.Head)
	}
	contents, err := os.ReadFile(filepath.Join(clone, "source.txt"))
	if err != nil || string(contents) != "first capture\n" {
		t.Fatal("capture followed mutable workspace", string(contents), err)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != "later uncheckpointed source\n" {
		t.Fatal("bundle import modified shared checkout", string(current), err)
	}
}

func TestGitSnapshotRejectsCancelledCaptureAndPathSelection(t *testing.T) {
	executor := NewGitSnapshotExecutor(tool.NewLocalExecutor(tool.Set{}, nil), t.TempDir(), t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := executor.Execute(ctx, tool.Request{ToolName: GitSnapshotTool}); err == nil {
		t.Fatal("cancelled capture executed")
	}
	for _, path := range []string{"/snapshots/../secret", "/snapshots/" + strings.Repeat("x", 64), "/snapshots/" + strings.Repeat("a", 64) + "/file"} {
		recorder := httptest.NewRecorder()
		executor.ArtifactHandler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != 400 {
			t.Fatal("artifact handler accepted non-identity path", path, recorder.Code)
		}
	}
}
