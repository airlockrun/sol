package executor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/airlockrun/goai/tool"
)

// mockTool creates a simple tool for testing.
func mockTool(name string) tool.Tool {
	return tool.Tool{
		Name:        name,
		Description: "Test tool: " + name,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
		Execute: func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args struct {
				Input string `json:"input"`
			}
			json.Unmarshal(input, &args)
			return tool.Result{
				Output: "echo: " + args.Input,
				Title:  name,
			}, nil
		},
	}
}

func TestRemoteExecutor(t *testing.T) {
	// Create local executor with test tools
	tools := make(tool.Set)
	tools.Add(mockTool("test_tool"))
	localExec := tool.NewLocalExecutor(tools, nil)

	// Create server
	server := NewToolServer(localExec)

	// Start test HTTP server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Convert HTTP URL to WebSocket URL
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect client
	transport, err := NewWSTransport(wsURL)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer transport.Close()

	// Fetch tools from server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	remoteTools, err := transport.FetchTools(ctx)
	if err != nil {
		t.Fatalf("Failed to fetch tools: %v", err)
	}

	if len(remoteTools) != 1 {
		t.Fatalf("Expected 1 tool, got %d", len(remoteTools))
	}

	if remoteTools[0].Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got '%s'", remoteTools[0].Name)
	}

	// Create remote executor
	remote := NewRemoteExecutor(transport, remoteTools)

	// Execute tool
	resp, err := remote.Execute(ctx, tool.Request{
		ToolCallID: "call_1",
		ToolName:   "test_tool",
		Input:      json.RawMessage(`{"input":"hello"}`),
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if resp.IsError {
		t.Errorf("Unexpected error: %s", resp.Output)
	}

	if resp.Output != "echo: hello" {
		t.Errorf("Expected 'echo: hello', got '%s'", resp.Output)
	}

	if resp.Title != "test_tool" {
		t.Errorf("Expected title 'test_tool', got '%s'", resp.Title)
	}
}

func TestRemoteExecutor_ToolNotFound(t *testing.T) {
	// Create local executor with no tools
	tools := make(tool.Set)
	localExec := tool.NewLocalExecutor(tools, nil)

	// Create server
	server := NewToolServer(localExec)

	// Start test HTTP server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Convert HTTP URL to WebSocket URL
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect client
	transport, err := NewWSTransport(wsURL)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer transport.Close()

	remote := NewRemoteExecutor(transport, nil)

	// Execute non-existent tool
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := remote.Execute(ctx, tool.Request{
		ToolCallID: "call_1",
		ToolName:   "nonexistent",
		Input:      json.RawMessage(`{}`),
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !resp.IsError {
		t.Error("Expected error response for nonexistent tool")
	}

	if !strings.Contains(resp.Output, "not found") {
		t.Errorf("Expected 'not found' in error, got '%s'", resp.Output)
	}
}

func TestRemoteExecutor_MultipleRequests(t *testing.T) {
	// Create local executor
	tools := make(tool.Set)
	tools.Add(mockTool("tool_a"))
	tools.Add(mockTool("tool_b"))
	localExec := tool.NewLocalExecutor(tools, nil)

	// Create server
	server := NewToolServer(localExec)

	// Start test HTTP server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Convert HTTP URL to WebSocket URL
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect client
	transport, err := NewWSTransport(wsURL)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer transport.Close()

	remote := NewRemoteExecutor(transport, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Execute multiple requests
	for i, name := range []string{"tool_a", "tool_b", "tool_a"} {
		resp, err := remote.Execute(ctx, tool.Request{
			ToolCallID: "call_" + string(rune('1'+i)),
			ToolName:   name,
			Input:      json.RawMessage(`{"input":"test"}`),
		})

		if err != nil {
			t.Fatalf("Request %d failed: %v", i, err)
		}

		if resp.IsError {
			t.Errorf("Request %d: unexpected error: %s", i, resp.Output)
		}

		if resp.Title != name {
			t.Errorf("Request %d: expected title '%s', got '%s'", i, name, resp.Title)
		}
	}
}

func TestRemoteExecutor_ClosedTransportIsFatal(t *testing.T) {
	tools := make(tool.Set)
	tools.Add(mockTool("test_tool"))
	localExec := tool.NewLocalExecutor(tools, nil)
	server := NewToolServer(localExec)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	transport, err := NewWSTransport(wsURL)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	remote := NewRemoteExecutor(transport, nil)

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = remote.Execute(ctx, tool.Request{
		ToolCallID: "call_1",
		ToolName:   "test_tool",
		Input:      json.RawMessage(`{"input":"hello"}`),
	})
	if err == nil {
		t.Fatal("expected fatal transport error, got nil")
	}

	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("expected TransportError, got %T: %v", err, err)
	}
	var fatalErr tool.FatalToolError
	if !errors.As(err, &fatalErr) || !fatalErr.FatalToolError() {
		t.Fatalf("expected FatalToolError, got %T: %v", err, err)
	}
}
