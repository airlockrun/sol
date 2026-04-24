package executor

import (
	"context"

	"github.com/airlockrun/goai/tool"
)

// RemoteExecutor implements tool.Executor by forwarding requests
// over a Transport to a remote ToolServer.
type RemoteExecutor struct {
	transport Transport
	tools     []tool.Info
}

// NewRemoteExecutor creates a RemoteExecutor with the given transport and tool definitions.
// The tools parameter provides the tool schemas that the LLM needs to see.
func NewRemoteExecutor(transport Transport, tools []tool.Info) *RemoteExecutor {
	return &RemoteExecutor{
		transport: transport,
		tools:     tools,
	}
}

// Execute implements tool.Executor by sending the request over the transport.
func (e *RemoteExecutor) Execute(ctx context.Context, req tool.Request) (tool.Response, error) {
	return e.transport.Send(ctx, req)
}

// Tools implements tool.Executor by returning the cached tool definitions.
func (e *RemoteExecutor) Tools() []tool.Info {
	return e.tools
}

// Close closes the underlying transport.
func (e *RemoteExecutor) Close() error {
	return e.transport.Close()
}
