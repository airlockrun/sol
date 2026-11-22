package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/tool"
)

const defaultKimiModel = "kimi-k2.6"
const kimiMaxRounds = 3

func (c *modelClient) searchKimi(ctx context.Context, model stream.Model, req Request) (*Response, error) {
	messages := []message.Message{message.NewUserMessage(req.Query)}
	tools := tool.Set{"$web_search": {Name: "$web_search", Type: "provider", ProviderID: "moonshotai.web_search"}}
	seen := map[string]bool{}
	var results []Result
	for round := 0; round < kimiMaxRounds; round++ {
		result, err := goai.GenerateText(ctx, stream.Input{Model: model, Messages: messages, Tools: tools})
		if err != nil {
			return nil, fmt.Errorf("websearch/kimi: %w", err)
		}
		for _, source := range sourcesToResults(result.Sources) {
			if !seen[source.URL] {
				seen[source.URL] = true
				results = append(results, source)
			}
		}
		if result.FinishReason != stream.FinishReasonToolCalls {
			return &Response{Results: results, Synthesis: result.Text, Provider: "kimi"}, nil
		}
		parts := []message.Part{}
		for _, reasoning := range result.Steps[len(result.Steps)-1].Reasoning() {
			parts = append(parts, message.ReasoningPart{Text: reasoning.Text, ProviderOptions: reasoning.ProviderOptions})
		}
		if result.Text != "" {
			parts = append(parts, message.TextPart{Text: result.Text})
		}
		for _, tc := range result.ToolCalls {
			if tc.Name != "$web_search" {
				return nil, fmt.Errorf("websearch/kimi: unexpected builtin tool %q", tc.Name)
			}
			parts = append(parts, message.ToolCallPart{ID: tc.ID, Name: tc.Name, Input: tc.Input})
			var args struct {
				SearchResults []struct {
					Title   string `json:"title"`
					URL     string `json:"url"`
					Content string `json:"content"`
				} `json:"search_results"`
			}
			if err := json.Unmarshal(tc.Input, &args); err != nil {
				return nil, fmt.Errorf("websearch/kimi: invalid builtin result: %w", err)
			}
			for _, r := range args.SearchResults {
				if r.URL != "" && !seen[r.URL] {
					seen[r.URL] = true
					results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
				}
			}
		}
		messages = append(messages, message.Message{Role: message.RoleAssistant, Content: message.Content{Parts: parts}})
		for _, tc := range result.ToolCalls {
			// Moonshot executes the builtin; its arguments are the result that the
			// client echoes verbatim to allow the model to continue.
			messages = append(messages, message.NewToolMessage(tc.ID, tc.Name, message.TextOutput{Value: string(tc.Input)}))
		}
	}
	return nil, errors.New("websearch/kimi: builtin search exceeded round limit")
}
