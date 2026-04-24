package sol

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/airlockrun/goai/mcp"
	"github.com/airlockrun/goai/tool"
)

// MCPServer configures an HTTP MCP server connection.
type MCPServer struct {
	// Name is a unique identifier for this server (used as tool name prefix).
	Name string

	// URL is the HTTP endpoint for the MCP server.
	URL string

	// Headers are HTTP headers (e.g., Authorization).
	Headers map[string]string
}

// ConnectMCPServers connects to the configured MCP servers via HTTP
// and returns the MCP client and the discovered tools.
// Tool schemas are normalized to match opencode's behavior.
// The caller must call client.DisconnectAll() when done.
func ConnectMCPServers(ctx context.Context, servers []MCPServer) (*mcp.Client, tool.Set, error) {
	client := mcp.NewClient()
	for _, s := range servers {
		err := client.Connect(ctx, mcp.ServerConfig{
			Name:      s.Name,
			Transport: "http",
			URL:       s.URL,
			Headers:   s.Headers,
		})
		if err != nil {
			client.DisconnectAll()
			return nil, nil, fmt.Errorf("MCP server %q: %w", s.Name, err)
		}
	}

	// Normalize MCP tool schemas to match opencode.
	// See opencode: packages/opencode/src/mcp/index.ts lines 125-129.
	tools := client.GetTools()
	for name, t := range tools {
		t.InputSchema = normalizeMCPToolSchema(t.InputSchema)
		tools[name] = t
	}

	return client, tools, nil
}

// normalizeMCPToolSchema ensures MCP tool schemas match opencode's normalization:
// force type "object", ensure "properties" exists, set "additionalProperties" to false.
func normalizeMCPToolSchema(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 {
		return schema
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(schema, &obj); err != nil {
		return schema
	}

	changed := false

	if _, ok := obj["type"]; !ok {
		obj["type"] = json.RawMessage(`"object"`)
		changed = true
	}

	if _, ok := obj["properties"]; !ok {
		obj["properties"] = json.RawMessage(`{}`)
		changed = true
	}

	if _, ok := obj["additionalProperties"]; !ok {
		obj["additionalProperties"] = json.RawMessage(`false`)
		changed = true
	}

	if !changed {
		return schema
	}

	normalized, err := json.Marshal(obj)
	if err != nil {
		return schema
	}
	return normalized
}
