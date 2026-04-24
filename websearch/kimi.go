package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	defaultKimiBaseURL = "https://api.moonshot.ai/v1"
	defaultKimiModel   = "moonshot-v1-128k"
	kimiMaxRounds      = 3
)

type kimiRequest struct {
	Model    string        `json:"model"`
	Messages []kimiMessage `json:"messages"`
	Tools    []kimiTool    `json:"tools"`
}

type kimiMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []kimiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type kimiTool struct {
	Type     string       `json:"type"`
	Function kimiFunction `json:"function"`
}

type kimiFunction struct {
	Name string `json:"name"`
}

type kimiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type kimiResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []kimiToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	SearchResults []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"search_results"`
}

func (c *DirectClient) searchKimi(ctx context.Context, req Request) (*Response, error) {
	model := c.model
	if model == "" {
		model = defaultKimiModel
	}

	endpoint := defaultKimiBaseURL + "/chat/completions"

	messages := []kimiMessage{{Role: "user", Content: req.Query}}
	tools := []kimiTool{{
		Type:     "builtin_function",
		Function: kimiFunction{Name: "$web_search"},
	}}

	seen := map[string]bool{}
	var results []Result
	var synthesis string

	for round := 0; round < kimiMaxRounds; round++ {
		body := kimiRequest{
			Model:    model,
			Messages: messages,
			Tools:    tools,
		}

		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("websearch/kimi: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("websearch/kimi: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.http.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("websearch/kimi: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			return nil, fmt.Errorf("websearch/kimi: HTTP %d: %s", resp.StatusCode, respBody)
		}

		var data kimiResponse
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, fmt.Errorf("websearch/kimi: %w", err)
		}

		// Collect citations from search_results.
		for _, sr := range data.SearchResults {
			if sr.URL != "" && !seen[sr.URL] {
				results = append(results, Result{
					Title:   sr.Title,
					URL:     sr.URL,
					Snippet: sr.Content,
				})
				seen[sr.URL] = true
			}
		}

		if len(data.Choices) == 0 {
			break
		}

		choice := data.Choices[0]

		// If finish_reason is not tool_calls, we have the final answer.
		if choice.FinishReason != "tool_calls" {
			synthesis = choice.Message.Content
			break
		}

		// Continue the multi-round loop: add assistant message + tool results.
		assistantMsg := kimiMessage{
			Role:      "assistant",
			ToolCalls: choice.Message.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		// Add tool result messages for each tool call.
		for _, tc := range choice.Message.ToolCalls {
			// Extract any search results from tool call arguments.
			var args struct {
				SearchResults []struct {
					URL string `json:"url"`
				} `json:"search_results"`
			}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			for _, sr := range args.SearchResults {
				if sr.URL != "" && !seen[sr.URL] {
					results = append(results, Result{URL: sr.URL})
					seen[sr.URL] = true
				}
			}

			// Acknowledge the tool call so the model continues.
			toolMsg := kimiMessage{
				Role:       "tool",
				Content:    "ok",
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolMsg)
		}
	}

	return &Response{
		Results:   results,
		Synthesis: synthesis,
		Provider:  "kimi",
	}, nil
}
