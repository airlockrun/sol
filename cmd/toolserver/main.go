// Command toolserver runs Sol's tools in a container, serving requests via WebSocket.
// This is the entrypoint for the airlock-toolserver container image.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/executor"
	"github.com/airlockrun/sol/tools"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	spaceDir := flag.String("space-dir", "/home/agent/space", "space directory (overlayfs merged mount)")
	homeDir := flag.String("home-dir", "", "set HOME environment variable to this path")
	flag.Parse()

	// Set HOME if specified (container bind-mounts a per-run home directory)
	if *homeDir != "" {
		os.Setenv("HOME", *homeDir)
	}

	// Read and delete the space token file (written by DockerManager before start).
	// Token is used to authenticate incoming WebSocket connections.
	spaceToken := readAndDeleteToken(*homeDir)

	// Working directory is the space dir itself (overlayfs merged view)
	workDir := *spaceDir

	// Start with all tools — airlock sends set_active_tools to filter
	toolSet := tools.CreateAllTools(workDir)

	// Create local executor with workdir context injection
	localExec := tool.NewLocalExecutor(toolSet, nil)
	wrappedExec := &workDirExecutor{
		inner:   localExec,
		workDir: workDir,
	}

	// Create and start ToolServer
	server := executor.NewToolServer(wrappedExec)

	// Mux with health endpoint
	mux := http.NewServeMux()
	mux.Handle("/ws", requireBearerToken(spaceToken, server.Handler()))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:    *addr,
		Handler: mux,
	}

	// Graceful shutdown on SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		fmt.Printf("toolserver listening on %s (workdir=%s)\n", *addr, workDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

// readAndDeleteToken reads $HOME/.space-token and deletes it.
// Returns empty string if the file doesn't exist (dev mode / no auth).
func readAndDeleteToken(homeDir string) string {
	if homeDir == "" {
		return ""
	}
	tokenPath := filepath.Join(homeDir, ".space-token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return ""
	}
	os.Remove(tokenPath)
	return strings.TrimSpace(string(data))
}

// requireBearerToken wraps an http.Handler and validates the Authorization header.
// If token is empty, auth is disabled (dev mode).
func requireBearerToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// workDirExecutor wraps a tool.Executor and injects WorkDir into every request.
type workDirExecutor struct {
	inner   tool.Executor
	workDir string
}

func (e *workDirExecutor) Execute(ctx context.Context, req tool.Request) (tool.Response, error) {
	req.WorkDir = e.workDir
	return e.inner.Execute(ctx, req)
}

func (e *workDirExecutor) Tools() []tool.Info {
	return e.inner.Tools()
}
