package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/airlockrun/goai/tool"
)

// ToolInput is the JSON schema for the web_search tool.
type ToolInput struct {
	Query     string `json:"query" description:"Search query string"`
	Count     int    `json:"count,omitempty" description:"Number of results to return (1-10, default 5)"`
	Freshness string `json:"freshness,omitempty" description:"Time filter: day, week, month, or year" enum:"day,week,month,year"`
	Country   string `json:"country,omitempty" description:"2-letter country code for region-specific results (e.g. US, DE)"`
	Language  string `json:"language,omitempty" description:"ISO 639-1 language code for results (e.g. en, de, fr)"`
}

// NewTool wraps a Client as a tool.Tool for use in Sol's agent loop or goai.
// The client can be any implementation — direct API, proxy through Airlock, etc.
func NewTool(client Client) tool.Tool {
	return tool.New("web_search").
		Description(`Search the web and return results. Returns titles, URLs, and snippets.
Some providers also return a synthesized summary alongside source citations.
Use this tool when you need current information from the internet.`).
		SchemaFromStruct(ToolInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args ToolInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			resp, err := client.Search(ctx, Request{
				Query:     args.Query,
				Count:     args.Count,
				Freshness: args.Freshness,
				Country:   args.Country,
				Language:  args.Language,
			})
			if err != nil {
				return tool.Result{
					Output: fmt.Sprintf("Search error: %v", err),
					Title:  "web_search",
				}, nil
			}

			output := formatResponse(args.Query, resp)

			return tool.Result{
				Output: output,
				Title:  fmt.Sprintf("web_search: %s", truncate(args.Query, 50)),
			}, nil
		}).
		Build()
}

func formatResponse(query string, resp *Response) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Web Search Results for %q (provider: %s)\n\n", query, resp.Provider)

	if resp.Synthesis != "" {
		b.WriteString("**Summary:**\n")
		b.WriteString(resp.Synthesis)
		b.WriteString("\n\n")

		if len(resp.Results) > 0 {
			b.WriteString("**Sources:**\n")
			for i, r := range resp.Results {
				if r.Title != "" {
					fmt.Fprintf(&b, "%d. %s — %s\n", i+1, r.Title, r.URL)
				} else {
					fmt.Fprintf(&b, "%d. %s\n", i+1, r.URL)
				}
			}
		}
	} else {
		for i, r := range resp.Results {
			fmt.Fprintf(&b, "%d. **%s**\n", i+1, r.Title)
			fmt.Fprintf(&b, "   URL: %s\n", r.URL)
			if r.Snippet != "" {
				fmt.Fprintf(&b, "   %s\n", r.Snippet)
			}
			b.WriteString("\n")
		}
		if len(resp.Results) == 0 {
			b.WriteString("No results found.\n")
		}
	}

	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
