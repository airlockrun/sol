package webfetch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/toolutil"
)

// ToolInput is the input schema for the webfetch tool.
type ToolInput struct {
	URL     string `json:"url" description:"The URL to fetch content from"`
	Format  string `json:"format" description:"The format to return the content in (text, markdown, or html). Defaults to markdown." enum:"text,markdown,html" default:"markdown"`
	Timeout int    `json:"timeout,omitempty" description:"Optional timeout in seconds (max 120)"`
}

// NewTool creates a webfetch tool backed by the given Client.
func NewTool(client Client) tool.Tool {
	return tool.New("webfetch").
		Description(`- Fetches content from a specified URL
- Takes a URL and optional format as input
- Fetches the URL content, converts to requested format (markdown by default)
- Returns the content in the specified format
- Use this tool when you need to retrieve and analyze web content

Usage notes:
  - The URL must be a fully-formed valid URL
  - HTTP URLs will be automatically upgraded to HTTPS
  - Format options: "markdown" (default), "text", or "html"
  - This tool is read-only and does not modify any files
`).
		SchemaFromStruct(ToolInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args ToolInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			resp, err := client.Fetch(ctx, Request{
				URL:     args.URL,
				Format:  args.Format,
				Timeout: args.Timeout,
			})
			if err != nil {
				return tool.Result{
					Output: fmt.Sprintf("Error: %v", err),
					Title:  truncateStr(args.URL, 50),
				}, nil
			}

			title := fmt.Sprintf("%s (%s)", resp.URL, resp.ContentType)
			truncResult := toolutil.TruncateOutput(resp.Content, toolutil.TruncateOptions{
				MaxLines: toolutil.TruncateMaxLines,
				MaxBytes: toolutil.TruncateMaxBytes,
			})
			return tool.Result{
				Output: truncResult.Content,
				Title:  title,
			}, nil
		}).
		Build()
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
