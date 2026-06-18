package websearch

import (
	"context"
	"fmt"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/provider/xai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
)

// defaultGrokModel is the last-resort xAI model for grok-backed web search, used
// only when pickSearchModel can't reach the live catalog (offline). xAI routes
// the hosted web_search tool through the Responses API; we pin a non-reasoning
// variant for latency. Online, pickSearchModel resolves the current one.
const defaultGrokModel = "grok-4.20-0309-non-reasoning"

// searchGrok runs a web-search-grounded generation through goai's xAI
// Responses provider with the xai.web_search hosted tool. Citations
// arrive as SourceEvents from url_citation annotations on the response.
func (c *DirectClient) searchGrok(ctx context.Context, req Request) (*Response, error) {
	model := c.model
	if model == "" {
		model = pickSearchModel("xai", []string{"fast", "mini", "non-reasoning"})
	}
	if model == "" {
		model = defaultGrokModel
	}

	p := xai.New(xai.Options{APIKey: c.apiKey})

	tools := tool.Set{}
	ws := xai.WebSearch()
	ws.Name = "xai_web_search"
	tools.Add(ws)

	result, err := goai.GenerateText(ctx, stream.Input{
		Model: p.Model(model),
		Tools: tools,
		Messages: []message.Message{
			message.NewUserMessage(req.Query),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("websearch/grok: %w", err)
	}

	return &Response{
		Results:   sourcesToResults(result.Sources),
		Synthesis: result.Text,
		Provider:  "grok",
	}, nil
}
