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
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
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
