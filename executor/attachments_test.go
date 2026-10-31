package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/airlockrun/goai/tool"
	"github.com/coder/websocket"
)

func TestAttachmentCapabilityDoesNotMaterializeAtStartup(t *testing.T) {
	server := httptest.NewServer(NewToolServer(tool.NewLocalExecutor(tool.Set{}, nil)).Handler())
	defer server.Close()
	transport, err := NewWSTransport("ws" + strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if err := transport.RequireAttachmentMaterialization(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteAttachmentMaterializationUsesExecutorFilesystemAndScope(t *testing.T) {
	var downloads atomic.Int32
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); io.WriteString(w, "exact bytes") }))
	defer storage.Close()
	digest := sha256.Sum256([]byte("exact bytes"))
	request := AttachmentMaterialization{Scope: "00000000-0000-4000-8000-000000000001", ID: "00000000-0000-4000-8000-000000000002", Filename: "design notes.txt", Size: 11, SHA256: hex.EncodeToString(digest[:]), URL: storage.URL + "/immutable", Origin: storage.URL}
	server := NewToolServer(tool.NewLocalExecutor(tool.Set{}, nil))
	server.attachmentRoot = t.TempDir()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	transport, err := NewWSTransport("ws" + strings.TrimPrefix(httpServer.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	remote := NewRemoteExecutor(transport, nil)
	result, err := remote.MaterializeAttachment(t.Context(), request)
	if err != nil || result.Reused || result.Path != filepath.Join(server.attachmentRoot, request.ID, request.Filename) {
		t.Fatalf("remote result=%+v err=%v", result, err)
	}
	if data, err := os.ReadFile(result.Path); err != nil || string(data) != "exact bytes" {
		t.Fatalf("executor content=%q err=%v", data, err)
	}
	if result, err := remote.MaterializeAttachment(t.Context(), request); err != nil || !result.Reused || downloads.Load() != 1 {
		t.Fatalf("remote reuse=%+v err=%v downloads=%d", result, err, downloads.Load())
	}
	request.Scope = "00000000-0000-4000-8000-000000000003"
	if _, err := remote.MaterializeAttachment(t.Context(), request); err == nil || !strings.Contains(err.Error(), "scope mismatch") {
		t.Fatalf("executor accepted another session scope: %v", err)
	}
	if downloads.Load() != 1 {
		t.Fatal("scope rejection fetched bytes")
	}
}

func TestAttachmentTransportCancellationWaitsForRemoteSettlement(t *testing.T) {
	started, cancelled, settle, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var request message
		if err := json.Unmarshal(data, &request); err != nil {
			t.Error(err)
			return
		}
		if request.Type != "materialize_attachment" || request.Attachment == nil {
			t.Errorf("wrong control request: %+v", request)
			return
		}
		close(started)
		_, data, err = conn.Read(r.Context())
		if err != nil {
			return
		}
		var cancellation message
		if err := json.Unmarshal(data, &cancellation); err != nil {
			t.Error(err)
			return
		}
		if cancellation.Type != "cancel" || cancellation.CancelID != request.ID {
			t.Errorf("wrong cancellation: %+v", cancellation)
			return
		}
		close(cancelled)
		<-settle
		data, _ = json.Marshal(message{Type: "response", ID: request.ID, Error: "download cancelled"})
		_ = conn.Write(context.Background(), websocket.MessageText, data)
		<-finished
	}))
	defer server.Close()
	transport, err := NewWSTransport("ws" + strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	defer close(finished)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := transport.MaterializeAttachment(ctx, AttachmentMaterialization{ID: "attachment"})
		done <- err
	}()
	<-started
	cancel()
	<-cancelled
	select {
	case err := <-done:
		t.Fatalf("released owner before remote download settled: %v", err)
	default:
	}
	close(settle)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestAttachmentMaterializationChecksActualExecutorBytes(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("credential sent to storage")
		}
		io.WriteString(w, "exact bytes")
	}))
	defer server.Close()
	digest := sha256.Sum256([]byte("exact bytes"))
	request := AttachmentMaterialization{Scope: "00000000-0000-4000-8000-000000000001", ID: "00000000-0000-4000-8000-000000000002", Filename: "design notes.txt", Size: 11, SHA256: hex.EncodeToString(digest[:]), URL: server.URL + "/exact-object?grant=short-lived", Origin: server.URL}
	root := t.TempDir()
	first, err := materializeAttachment(t.Context(), root, request)
	if err != nil || first.Reused || downloads.Load() != 1 {
		t.Fatalf("first=%+v err=%v downloads=%d", first, err, downloads.Load())
	}
	again, err := materializeAttachment(t.Context(), root, request)
	if err != nil || !again.Reused || downloads.Load() != 1 {
		t.Fatalf("reuse=%+v err=%v downloads=%d", again, err, downloads.Load())
	}
	if err := os.WriteFile(first.Path, []byte("wrong bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	tampered, err := materializeAttachment(t.Context(), root, request)
	if err != nil || tampered.Reused || downloads.Load() != 2 {
		t.Fatalf("tampered=%+v err=%v downloads=%d", tampered, err, downloads.Load())
	}
	if err := os.Remove(first.Path); err != nil {
		t.Fatal(err)
	}
	missing, err := materializeAttachment(t.Context(), root, request)
	if err != nil || missing.Reused || downloads.Load() != 3 {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	// A separate executor has no shared Airlock-host cache or filesystem.
	isolated, err := materializeAttachment(t.Context(), t.TempDir(), request)
	if err != nil || isolated.Reused || downloads.Load() != 4 {
		t.Fatalf("isolated=%+v err=%v", isolated, err)
	}
}

func TestAttachmentMaterializationRejectsUntrustedOrInvalidDownloads(t *testing.T) {
	for _, tc := range []struct {
		name, body        string
		redirect, symlink bool
		mutate            func(*AttachmentMaterialization)
	}{
		{name: "digest mismatch", body: "wrong bytes"},
		{name: "short", body: "short"},
		{name: "oversized", body: "exact bytes!"},
		{name: "redirect", redirect: true},
		{name: "symlink directory", body: "exact bytes", symlink: true},
		{name: "traversal", body: "exact bytes", mutate: func(r *AttachmentMaterialization) { r.Filename = "../source.go" }},
		{name: "wrong origin", body: "exact bytes", mutate: func(r *AttachmentMaterialization) { r.Origin = "https://other.example" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.redirect {
					http.Redirect(w, r, "https://other.example", http.StatusFound)
					return
				}
				w.Header().Set("Trailer", "X-End")
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			digest := sha256.Sum256([]byte("exact bytes"))
			request := AttachmentMaterialization{Scope: "00000000-0000-4000-8000-000000000001", ID: "00000000-0000-4000-8000-000000000002", Filename: "note.txt", Size: 11, SHA256: hex.EncodeToString(digest[:]), URL: server.URL, Origin: server.URL}
			if tc.mutate != nil {
				tc.mutate(&request)
			}
			root := t.TempDir()
			outside := t.TempDir()
			if tc.symlink {
				if err := os.Symlink(outside, filepath.Join(root, request.ID)); err != nil {
					t.Fatal(err)
				}
			}
			if result, err := materializeAttachment(t.Context(), root, request); err == nil {
				t.Fatalf("invalid request accepted: %+v", result)
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("download escaped executor attachment root")
			}
			filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err == nil && strings.HasPrefix(entry.Name(), ".download-") {
					t.Errorf("temporary file leaked: %s", path)
				}
				return err
			})
		})
	}
}
