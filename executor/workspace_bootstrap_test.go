package executor

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bootstrapArchive(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExtractBootstrapRejectsUnsafeInputs(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers []*tar.Header
	}{
		{"traversal", []*tar.Header{{Name: "../outside", Typeflag: tar.TypeReg}}},
		{"absolute", []*tar.Header{{Name: "/outside", Typeflag: tar.TypeReg}}},
		{"normalized escape", []*tar.Header{{Name: "a/../../outside", Typeflag: tar.TypeReg}}},
		{"backslash", []*tar.Header{{Name: "a\\outside", Typeflag: tar.TypeReg}}},
		{"symlink", []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/outside"}}},
		{"hardlink", []*tar.Header{{Name: "link", Typeflag: tar.TypeLink, Linkname: "/outside"}}},
		{"duplicate", []*tar.Header{{Name: "file", Typeflag: tar.TypeReg}, {Name: "file", Typeflag: tar.TypeReg}}},
		{"path limit", []*tar.Header{{Name: strings.Repeat("x", 1025), Typeflag: tar.TypeReg}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := extractBootstrap(bytes.NewReader(bootstrapArchive(t, test.headers...)), t.TempDir()); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	if err := extractBootstrap(strings.NewReader("not a tar archive"), t.TempDir()); err == nil {
		t.Fatal("corrupt archive accepted")
	}
}

func TestWorkspaceBootstrapCrashRetryIntegrityAndStalePublication(t *testing.T) {
	input := bootstrapArchive(t, &tar.Header{Name: "value", Typeflag: tar.TypeReg, Size: 4})
	digest := fmt.Sprintf("%x", sha256.Sum256(input))
	command := []string{"/bin/sh", "-c", `mkdir "$2"; cp "$1/value" "$2/value"`, "initializer"}
	for _, test := range []string{"digest", "stale", "crash", "interrupted", "success"} {
		t.Run(test, func(t *testing.T) {
			root := t.TempDir()
			b := NewWorkspaceBootstrap(root, digest, command)
			reader := io.Reader(bytes.NewReader(input))
			check := func() error { return nil }
			ctx := t.Context()
			switch test {
			case "digest":
				reader = strings.NewReader("corrupt")
			case "stale":
				check = func() error { return ErrControllerReplaced }
			case "crash":
				if err := os.Mkdir(filepath.Join(root, ".bootstrap-crashed"), 0700); err != nil {
					t.Fatal(err)
				}
			case "interrupted":
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
				b.command = []string{"/bin/sh", "-c", `mkdir "$2"; sleep 30; cp "$1/value" "$2/value"`, "initializer"}
			}
			err := b.initialize(ctx, reader, check)
			if test == "digest" || test == "stale" || test == "interrupted" {
				if err == nil || b.Ready() {
					t.Fatal("failed initialization became ready", err)
				}
				b = NewWorkspaceBootstrap(root, digest, command)
				if err := b.initialize(t.Context(), bytes.NewReader(input), func() error { return nil }); err != nil {
					t.Fatal("retry", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !NewWorkspaceBootstrap(root, digest, command).Ready() {
				t.Fatal("restart lost exact initialization binding")
			}
			if _, err := os.Stat(filepath.Join(root, ".bootstrap-crashed")); !os.IsNotExist(err) {
				t.Fatal("abandoned staging survived retry", err)
			}
			if err := b.initialize(t.Context(), bytes.NewReader(input), func() error { return nil }); err == nil {
				t.Fatal("direct initializer overwrote published workspace")
			}
		})
	}
}

type bootstrapTestAuthority struct {
	*testControllerAuthority
	digest string
}

func (a bootstrapTestAuthority) Validate(ctx context.Context, identity ControllerIdentity, credential string) (ControllerGrant, error) {
	if strings.HasPrefix(credential, "init") {
		return ControllerGrant{}, errors.New("initialization is not execution authority")
	}
	return a.testControllerAuthority.Validate(ctx, identity, credential)
}
func (a bootstrapTestAuthority) ValidateBootstrap(ctx context.Context, identity ControllerIdentity, credential, digest string) (ControllerGrant, error) {
	if digest != a.digest || !strings.HasPrefix(credential, "init") {
		return ControllerGrant{}, errors.New("bootstrap scope denied")
	}
	return a.testControllerAuthority.Validate(ctx, identity, credential)
}

func TestWorkspaceBootstrapSeparateAuthorityAndConsumedAdmission(t *testing.T) {
	f := newControllerFixture(t)
	input := bootstrapArchive(t, &tar.Header{Name: "value", Typeflag: tar.TypeReg, Size: 4})
	digest := fmt.Sprintf("%x", sha256.Sum256(input))
	f.server.controller.authority = bootstrapTestAuthority{f.authority, digest}
	f.server.EnableWorkspaceBootstrap(NewWorkspaceBootstrap(t.TempDir(), digest, []string{"/bin/sh", "-c", `mkdir "$2"; cp "$1/value" "$2/value"`, "initializer"}))
	h := httptest.NewServer(f.server.WorkspaceBootstrapHandler())
	defer h.Close()
	request := func(credential string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, h.URL, bytes.NewReader(input))
		req.Header.Set(ControllerCredentialHeader, credential)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	f.authority.issue("exec", 1, time.Now().Add(time.Minute))
	if status := request("exec"); status != 403 {
		t.Fatal("execution token initialized workspace", status)
	}
	f.authority.issue("init", 2, time.Now().Add(time.Minute))
	if status := request("init"); status != 204 {
		t.Fatal("bootstrap", status)
	}
	if status := request("init"); status != 403 {
		t.Fatal("consumed initialization admission reused", status)
	}
	f.authority.issue("init-retry", 3, time.Now().Add(time.Minute))
	if status := request("init-retry"); status != 204 {
		t.Fatal("lost-ACK retry failed", status)
	}
	if _, err := f.server.controller.admit(t.Context(), "init-retry"); err == nil {
		t.Fatal("initialization credential admitted execution")
	}
	grant := f.authority.issue("exec-fresh", 4, time.Now().Add(time.Minute))
	conn, err := f.server.controller.admit(t.Context(), "exec-fresh")
	if err != nil || !sameAdmission(conn.grant, grant) {
		t.Fatal("fresh execution", err)
	}
	conn.cancel()
	close(conn.done)
}

func TestWorkspaceBootstrapTakeoverInterruptsStalledUploadAndJoins(t *testing.T) {
	f := newControllerFixture(t)
	input := bootstrapArchive(t, &tar.Header{Name: "value", Typeflag: tar.TypeReg, Size: 4})
	digest := fmt.Sprintf("%x", sha256.Sum256(input))
	f.server.controller.authority = bootstrapTestAuthority{f.authority, digest}
	f.server.EnableWorkspaceBootstrap(NewWorkspaceBootstrap(t.TempDir(), digest, []string{"/bin/sh", "-c", `mkdir "$2"; cp "$1/value" "$2/value"`, "initializer"}))
	h := httptest.NewServer(f.server.WorkspaceBootstrapHandler())
	defer h.Close()
	f.authority.issue("init-stalled", 1, time.Now().Add(time.Minute))
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, h.URL, reader)
		req.Header.Set(ControllerCredentialHeader, "init-stalled")
		response, err := http.DefaultClient.Do(req)
		if err == nil {
			response.Body.Close()
		}
	}()
	if _, err := writer.Write([]byte("partial upload")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		f.server.controller.mu.Lock()
		active := f.server.controller.active != nil
		f.server.controller.mu.Unlock()
		if active {
			break
		}
		select {
		case <-deadline:
			t.Fatal("stalled upload was not admitted")
		case <-time.After(time.Millisecond):
		}
	}
	f.authority.issue("init-successor", 2, time.Now().Add(time.Minute))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, h.URL, bytes.NewReader(input))
	req.Header.Set(ControllerCredentialHeader, "init-successor")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("successor could not join stalled upload", err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("successor bootstrap", response.StatusCode)
	}
	writer.Close()
	select {
	case <-oldDone:
	case <-ctx.Done():
		t.Fatal("stale uploader did not settle")
	}
	if !f.server.bootstrap.Ready() {
		t.Fatal("successor did not publish initialized workspace")
	}
}
