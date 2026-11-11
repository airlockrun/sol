package executor

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const WorkspaceBootstrapCapability = "workspace-bootstrap.v1"
const MaxBootstrapBytes int64 = 512 << 20

// BootstrapAuthority authenticates initialization separately from execution.
// The digest is an immutable input binding, not authority supplied by a client.
type BootstrapAuthority interface {
	ValidateBootstrap(context.Context, ControllerIdentity, string, string) (ControllerGrant, error)
}

// WorkspaceBootstrap publishes one initialized directory by atomic rename. The
// root is private container-owned storage; command is a trusted installed program
// accepting input-directory and output-directory arguments. Inputs are data only.
type WorkspaceBootstrap struct {
	root, digest string
	command      []string
}

func NewWorkspaceBootstrap(root, digest string, command []string) *WorkspaceBootstrap {
	decoded, err := hex.DecodeString(digest)
	if !filepath.IsAbs(root) || err != nil || len(decoded) != sha256.Size || len(command) == 0 {
		panic("executor: bootstrap requires absolute root, SHA-256 and initializer")
	}
	return &WorkspaceBootstrap{root: root, digest: digest, command: append([]string(nil), command...)}
}

func (b *WorkspaceBootstrap) Ready() bool {
	data, err := os.ReadFile(filepath.Join(b.root, "workspace", ".bootstrap-input"))
	return err == nil && string(data) == b.digest
}

// EnableWorkspaceBootstrap requires initialization before any ordinary socket.
func (s *ToolServer) EnableWorkspaceBootstrap(b *WorkspaceBootstrap) {
	if s.controller == nil || b == nil {
		panic("executor: bootstrap requires controlled server and storage")
	}
	if _, ok := s.controller.authority.(BootstrapAuthority); !ok {
		panic("executor: bootstrap authority required")
	}
	s.bootstrap = b
}

func (s *ToolServer) WorkspaceBootstrapHandler() http.Handler {
	if s.bootstrap == nil {
		panic("executor: bootstrap not configured")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b := s.bootstrap
		credential := r.Header.Get(ControllerCredentialHeader)
		validate := func(ctx context.Context, value string) (ControllerGrant, error) {
			return s.controller.authority.(BootstrapAuthority).ValidateBootstrap(ctx, s.controller.identity, value, b.digest)
		}
		conn, err := s.controller.admitWith(r.Context(), credential, validate)
		if err != nil {
			http.Error(w, "bootstrap admission denied", http.StatusForbidden)
			return
		}
		defer close(conn.done)
		defer conn.cancel()
		ctx, cancel := context.WithTimeout(conn.ctx, 5*time.Minute)
		defer cancel()
		readControl := http.NewResponseController(w)
		if err := readControl.SetReadDeadline(time.Now().Add(5 * time.Minute)); err != nil {
			http.Error(w, "bootstrap requires bounded request reads", http.StatusInternalServerError)
			return
		}
		stopRead := context.AfterFunc(ctx, func() { _ = readControl.SetReadDeadline(time.Now()) })
		defer stopRead()
		if b.Ready() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := b.initialize(ctx, io.LimitReader(r.Body, MaxBootstrapBytes+1), func() error {
			grant, err := validate(conn.ctx, credential)
			if err != nil {
				return err
			}
			s.controller.mu.Lock()
			defer s.controller.mu.Unlock()
			if s.controller.active != conn || !sameAdmission(grant, conn.grant) || conn.ctx.Err() != nil {
				return ErrControllerReplaced
			}
			return nil
		}); err != nil {
			http.Error(w, "bootstrap failed; retry requires fresh admission", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (b *WorkspaceBootstrap) initialize(ctx context.Context, reader io.Reader, check func() error) error {
	if err := os.MkdirAll(b.root, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(b.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".bootstrap-") {
			if err := os.RemoveAll(filepath.Join(b.root, entry.Name())); err != nil {
				return err
			}
		}
	}
	stage, err := os.MkdirTemp(b.root, ".bootstrap-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archive, err := os.CreateTemp(stage, "input-*.tar")
	if err != nil {
		return err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(archive, hash), reader)
	closeErr := archive.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if n > MaxBootstrapBytes || hex.EncodeToString(hash.Sum(nil)) != b.digest {
		return errors.New("bootstrap artifact digest/size mismatch")
	}
	input := filepath.Join(stage, "input")
	if err := os.Mkdir(input, 0700); err != nil {
		return err
	}
	f, err := os.Open(archive.Name())
	if err != nil {
		return err
	}
	defer f.Close()
	if err := extractBootstrap(f, input); err != nil {
		return err
	}
	output := filepath.Join(stage, "output")
	args := append(append([]string{}, b.command[1:]...), input, output)
	cmd := exec.CommandContext(ctx, b.command[0], args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	configureBootstrapProcess(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("workspace initializer: %w", err)
	}
	if err := check(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, ".bootstrap-input"), []byte(b.digest), 0600); err != nil {
		return err
	}
	if err := syncBootstrapTree(output); err != nil {
		return err
	}
	// A ready directory is never overwritten, including after an unknown ACK.
	if _, err := os.Lstat(filepath.Join(b.root, "workspace")); !os.IsNotExist(err) {
		return errors.New("bootstrap target already exists")
	}
	if err := os.Rename(output, filepath.Join(b.root, "workspace")); err != nil {
		return err
	}
	return syncBootstrapPath(b.root)
}

func syncBootstrapPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func syncBootstrapTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return syncBootstrapPath(path)
	})
}

func extractBootstrap(reader io.Reader, root string) error {
	tr := tar.NewReader(reader)
	seen := map[string]bool{}
	var total int64
	for count := 0; ; count++ {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := h.Name
		if count >= 50000 || len(name) > 1024 || name == "." || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") || seen[name] {
			return errors.New("invalid bootstrap path/count")
		}
		seen[name] = true
		if h.Typeflag != tar.TypeReg || h.Size < 0 {
			return errors.New("bootstrap accepts regular files only")
		}
		total += h.Size
		if total > MaxBootstrapBytes {
			return errors.New("bootstrap contents too large")
		}
		target := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(f, tr, h.Size)
		if err := errors.Join(copyErr, f.Close()); err != nil {
			return err
		}
	}
}
