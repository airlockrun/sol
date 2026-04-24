// Package executor provides remote tool execution capabilities.
// It enables running tools in isolated containers while the LLM
// orchestration happens in the main backend.
package executor

import (
	"context"

	"github.com/airlockrun/goai/tool"
)

// Transport abstracts the communication layer between RemoteExecutor and ToolServer.
// Implementations can use WebSocket, HTTP, gRPC, or any other protocol.
type Transport interface {
	// Send sends a tool request and waits for the response.
	Send(ctx context.Context, req tool.Request) (tool.Response, error)

	// Close closes the transport connection.
	Close() error
}
