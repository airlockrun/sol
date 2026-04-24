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
	grokEndpoint    = "https://api.x.ai/v1/responses"
	defaultGrokModel = "grok-4-1-fast"
)

type grokRequest struct {
	Model string          `json:"model"`
	Input []grokMessage   `json:"input"`
	Tools []grokToolDef   `json:"tools"`
}

type grokMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type grokToolDef struct {
	Type string `json:"type"`
}

type grokResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content []struct {
			Type        string           `json:"type"`
			Text        string           `json:"text"`
			Annotations []grokAnnotation `json:"annotations"`
		} `json:"content"`
		Annotations []grokAnnotation `json:"annotations"`
	} `json:"output"`
	OutputText string `json:"output_text"` // deprecated fallback
}

type grokAnnotation struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func (c *DirectClient) searchGrok(ctx context.Context, req Request) (*Response, error) {
	model := c.model
	if model == "" {
		model = defaultGrokModel
	}

	body := grokRequest{
		Model: model,
		Input: []grokMessage{{Role: "user", Content: req.Query}},
		Tools: []grokToolDef{{Type: "web_search"}},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("websearch/grok: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", grokEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("websearch/grok: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("websearch/grok: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("websearch/grok: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var data grokResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("websearch/grok: %w", err)
	}

	synthesis, citations := extractGrokContent(data)

	results := make([]Result, 0, len(citations))
	for _, u := range citations {
		results = append(results, Result{URL: u})
	}

	return &Response{
		Results:   results,
		Synthesis: synthesis,
		Provider:  "grok",
	}, nil
}

// extractGrokContent extracts the synthesized text and citation URLs from a Grok response.
func extractGrokContent(data grokResponse) (text string, citations []string) {
	seen := map[string]bool{}

	for _, output := range data.Output {
		// Message type with content blocks
		if output.Type == "message" {
			for _, block := range output.Content {
				if block.Type == "output_text" && block.Text != "" {
					text = block.Text
					for _, a := range block.Annotations {
						if a.Type == "url_citation" && a.URL != "" && !seen[a.URL] {
							citations = append(citations, a.URL)
							seen[a.URL] = true
						}
					}
					return
				}
			}
		}
		// Top-level output_text block (no message wrapper)
		if output.Type == "output_text" && output.Text != "" {
			text = output.Text
			for _, a := range output.Annotations {
				if a.Type == "url_citation" && a.URL != "" && !seen[a.URL] {
					citations = append(citations, a.URL)
					seen[a.URL] = true
				}
			}
			return
		}
	}

	// Deprecated fallback
	text = data.OutputText
	return
}
