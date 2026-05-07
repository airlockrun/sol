package websearch

import (
	"context"
	"fmt"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/provider/google"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
)

// defaultGeminiModel is the default model for gemini-backed web search.
// Gemini 2.5 supports the google_search grounding tool.
const defaultGeminiModel = "gemini-2.5-flash"

// searchGemini runs a search-grounded generation through goai's Google
// provider with the google_search grounding tool. Citations arrive as
// SourceEvents from the response's groundingMetadata.groundingChunks
// (web variant), matching ai-sdk's extractSources behavior.
func (c *DirectClient) searchGemini(ctx context.Context, req Request) (*Response, error) {
	model := c.model
	if model == "" {
		model = defaultGeminiModel
	}

	p := google.New(google.Options{APIKey: c.apiKey})

	tools := tool.Set{}
	gs := google.GoogleSearch()
	gs.Name = "google_search"
	tools.Add(gs)

	result, err := goai.GenerateText(ctx, stream.Input{
		Model: p.Model(model),
		Tools: tools,
		Messages: []message.Message{
			message.NewUserMessage(req.Query),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("websearch/gemini: %w", err)
	}

	return &Response{
		Results:   sourcesToResults(result.Sources),
		Synthesis: result.Text,
		Provider:  "gemini",
	}, nil
}
