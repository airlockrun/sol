package websearch

import (
	"context"
	"fmt"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/provider"
	"github.com/airlockrun/goai/provider/openai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
)

// defaultOpenAIModel is the default model for OpenAI web search. We pick
// gpt-5-nano over gpt-5/gpt-5-mini for latency: web search runs through
// the Responses API and gpt-5 with default reasoning takes 30-60s on
// terse queries. Nano returns comparable citation coverage in ~10s,
// which fits comfortably under public-DM timeouts. Callers that want
// higher-quality synthesis can override via Options.Model.
const defaultOpenAIModel = "gpt-5-nano"

// searchOpenAI runs a web-search-grounded generation through goai's
// OpenAI Responses provider with the openai.web_search hosted tool.
// Citations are extracted from goai's SourceEvent stream — the same
// mechanism that surfaces url_citation annotations in ai-sdk's
// generateText.sources.
func (c *DirectClient) searchOpenAI(ctx context.Context, req Request) (*Response, error) {
	model := c.model
	if model == "" {
		model = defaultOpenAIModel
	}

	p := openai.New(provider.Options{APIKey: c.apiKey})

	tools := tool.Set{}
	ws := openai.WebSearch()
	ws.Name = "openai_web_search"
	tools.Add(ws)

	result, err := goai.GenerateText(ctx, stream.Input{
		Model:     p.Responses(model),
		Tools:     tools,
		Reasoning: stream.ReasoningEffortLow,
		Messages: []message.Message{
			message.NewSystemMessage("You are a web search assistant. Use the web_search tool to answer the user's query with up-to-date information and cite sources."),
			message.NewUserMessage(req.Query),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("websearch/openai: %w", err)
	}

	return &Response{
		Results:   sourcesToResults(result.Sources),
		Synthesis: result.Text,
		Provider:  "openai",
	}, nil
}

// sourcesToResults adapts goai's stream.SourceEvent (parity with
// ai-sdk's LanguageModelV4Source) to websearch's Result type. Document
// sources without a URL are dropped — websearch.Result is URL-shaped.
func sourcesToResults(sources []stream.SourceEvent) []Result {
	if len(sources) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(sources))
	out := make([]Result, 0, len(sources))
	for _, s := range sources {
		if s.URL == "" || seen[s.URL] {
			continue
		}
		seen[s.URL] = true
		out = append(out, Result{Title: s.Title, URL: s.URL})
	}
	return out
}
