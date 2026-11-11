package executor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/airlockrun/goai/tool"
)

// GitSnapshotTool is an executor-private, ordered callback. It is not exposed in
// the model tool catalogue. The embedding controller uses it to obtain immutable
// source without running host Git against a transferred mutable directory.
const GitSnapshotTool = "_executor_git_snapshot"
const GitSnapshotCapability = "git-snapshot.v1"

type GitSnapshot struct {
	ID     string `json:"id"`
	Head   string `json:"head"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type GitSnapshotExecutor struct {
	inner                tool.Executor
	workspace, artifacts string
}

func NewGitSnapshotExecutor(inner tool.Executor, workspace, artifacts string) *GitSnapshotExecutor {
	if inner == nil || !filepath.IsAbs(workspace) || !filepath.IsAbs(artifacts) {
		panic("executor: snapshot executor requires absolute workspace/artifact paths and executor")
	}
	return &GitSnapshotExecutor{inner: inner, workspace: workspace, artifacts: artifacts}
}
func (e *GitSnapshotExecutor) Tools() []tool.Info { return e.inner.Tools() }
func (e *GitSnapshotExecutor) Execute(ctx context.Context, request tool.Request) (tool.Response, error) {
	if request.ToolName != GitSnapshotTool {
		return e.inner.Execute(ctx, request)
	}
	snapshot, err := e.capture(ctx)
	if err != nil {
		return tool.Response{}, err
	}
	data, err := json.Marshal(snapshot)
	return tool.Response{Output: string(data)}, err
}
func (e *GitSnapshotExecutor) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = e.workspace
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("snapshot git %s: %w: %s", args[0], err, data)
	}
	return strings.TrimSpace(string(data)), nil
}
func (e *GitSnapshotExecutor) capture(ctx context.Context) (GitSnapshot, error) {
	if _, err := e.git(ctx, "add", "-A"); err != nil {
		return GitSnapshot{}, err
	}
	status, err := e.git(ctx, "status", "--porcelain")
	if err != nil {
		return GitSnapshot{}, err
	}
	if status != "" {
		if _, err := e.git(ctx, "-c", "core.hooksPath=/dev/null", "commit", "-m", "Development workspace snapshot"); err != nil {
			return GitSnapshot{}, err
		}
	}
	head, err := e.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return GitSnapshot{}, err
	}
	if err := os.MkdirAll(e.artifacts, 0700); err != nil {
		return GitSnapshot{}, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return GitSnapshot{}, err
	}
	id := hex.EncodeToString(nonce[:])
	path := filepath.Join(e.artifacts, id)
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if _, err := e.git(ctx, "bundle", "create", path, "HEAD", "--branches", "--tags"); err != nil {
		return GitSnapshot{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return GitSnapshot{}, err
	}
	defer f.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return GitSnapshot{}, err
	}
	keep = true
	return GitSnapshot{ID: id, Head: head, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

// ArtifactHandler serves immutable capture bytes. Its embedding host must
// authenticate resource-specific access. It never accepts a filesystem path.
func (e *GitSnapshotExecutor) ArtifactHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/snapshots/")
		data, err := hex.DecodeString(id)
		if r.Method != http.MethodGet || err != nil || len(data) != 32 || strings.Contains(id, "/") {
			http.Error(w, "invalid snapshot", http.StatusBadRequest)
			return
		}
		f, err := os.Open(filepath.Join(e.artifacts, id))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.Error(w, "invalid snapshot", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, id, info.ModTime(), f)
	})
}

func DecodeGitSnapshot(response tool.Response) (GitSnapshot, error) {
	var result GitSnapshot
	if response.IsError || response.Error != "" {
		return result, errors.New("executor source snapshot failed")
	}
	if err := json.Unmarshal([]byte(response.Output), &result); err != nil {
		return result, err
	}
	id, err := hex.DecodeString(result.ID)
	head, headErr := hex.DecodeString(result.Head)
	digest, digestErr := hex.DecodeString(result.SHA256)
	if err != nil || headErr != nil || digestErr != nil || len(id) != 32 || result.Size < 1 || (len(head) != 20 && len(head) != 32) || len(digest) != 32 {
		return result, errors.New("invalid source snapshot descriptor")
	}
	return result, nil
}

func (t *WSTransport) RequireGitSnapshot(ctx context.Context) error {
	response, err := t.roundTrip(ctx, message{Type: "tools", ID: t.allocID()})
	if err != nil {
		return err
	}
	for _, capability := range response.Capabilities {
		if capability == GitSnapshotCapability {
			return nil
		}
	}
	return errors.New("developer toolserver requires rebuild: missing git-snapshot.v1 capability; no source capture dispatched")
}
