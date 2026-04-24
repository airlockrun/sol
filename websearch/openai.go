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
	openaiEndpoint    = "https://api.openai.com/v1/responses"
	defaultOpenAIModel = "gpt-4o-mini"
)

type openaiRequest struct {
	Model      string          `json:"model"`
	Input      []openaiMessage `json:"input"`
	Tools      []openaiToolDef `json:"tools"`
	ToolChoice string          `json:"tool_choice,omitempty"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiToolDef struct {
	Type string `json:"type"` // "web_search"
}

type openaiResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content []struct {
			Type        string             `json:"type"`
			Text        string             `json:"text"`
			Annotations []openaiAnnotation `json:"annotations"`
		} `json:"content"`
		Annotations []openaiAnnotation `json:"annotations"`
	} `json:"output"`
	OutputText string `json:"output_text"` // deprecated fallback
}

type openaiAnnotation struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// searchOpenAI calls OpenAI's Responses API with the web_search tool enabled
// and returns the synthesized answer plus citation URLs. Mirrors the grok
// client — the Responses API shape is similar enough that only the endpoint,
// default model, and tool name differ.
func (c *DirectClient) searchOpenAI(ctx context.Context, req Request) (*Response, error) {
	model := c.model
	if model == "" {
		model = defaultOpenAIModel
	}

	body := openaiRequest{
		Model: model,
		Input: []openaiMessage{
			{
				Role:    "system",
				Content: "You are a web search assistant. Use the web_search tool to answer the user's query with up-to-date information and cite sources.",
			},
			{Role: "user", Content: req.Query},
		},
		Tools: []openaiToolDef{{Type: "web_search"}},
		// Force the model to call the tool — for vague queries like "X Y"
		// gpt-4o-mini will otherwise ask for clarification instead of
		// searching.
		ToolChoice: "required",
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("websearch/openai: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", openaiEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("websearch/openai: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("websearch/openai: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("websearch/openai: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var data openaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("websearch/openai: %w", err)
	}

	synthesis, results := extractOpenAIContent(data)

	return &Response{
		Results:   results,
		Synthesis: synthesis,
		Provider:  "openai",
	}, nil
}

// extractOpenAIContent walks the Responses API output and returns the
// assistant text plus deduped url_citation annotations.
func extractOpenAIContent(data openaiResponse) (text string, results []Result) {
	seen := map[string]bool{}

	appendCitation := func(a openaiAnnotation) {
		if a.Type != "url_citation" || a.URL == "" || seen[a.URL] {
			return
		}
		seen[a.URL] = true
		results = append(results, Result{Title: a.Title, URL: a.URL})
	}

	for _, output := range data.Output {
		// Message type with nested content blocks.
		if output.Type == "message" {
			for _, block := range output.Content {
				if block.Type == "output_text" && block.Text != "" {
					if text == "" {
						text = block.Text
					}
					for _, a := range block.Annotations {
						appendCitation(a)
					}
				}
			}
			continue
		}
		// Top-level output_text block (no message wrapper).
		if output.Type == "output_text" && output.Text != "" {
			if text == "" {
				text = output.Text
			}
			for _, a := range output.Annotations {
				appendCitation(a)
			}
		}
	}

	// Deprecated fallback.
	if text == "" {
		text = data.OutputText
	}
	return
}
