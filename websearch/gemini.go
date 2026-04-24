package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	geminiAPIBase     = "https://generativelanguage.googleapis.com/v1beta"
	defaultGeminiModel = "gemini-2.5-flash"
)

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
	Tools    []geminiTool    `json:"tools"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiTool struct {
	GoogleSearch *struct{} `json:"google_search"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		GroundingMetadata *struct {
			GroundingChunks []struct {
				Web *struct {
					URI   string `json:"uri"`
					Title string `json:"title"`
				} `json:"web"`
			} `json:"groundingChunks"`
		} `json:"groundingMetadata"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *DirectClient) searchGemini(ctx context.Context, req Request) (*Response, error) {
	model := c.model
	if model == "" {
		model = defaultGeminiModel
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent", geminiAPIBase, model)

	body := geminiRequest{
		Contents: []geminiContent{{
			Parts: []geminiPart{{Text: req.Query}},
		}},
		Tools: []geminiTool{{GoogleSearch: &struct{}{}}},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("websearch/gemini: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("websearch/gemini: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("websearch/gemini: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("websearch/gemini: HTTP %d: %s", resp.StatusCode, sanitizeGeminiError(string(respBody), c.apiKey))
	}

	var data geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("websearch/gemini: %w", err)
	}

	if data.Error != nil {
		return nil, fmt.Errorf("websearch/gemini: API error %d: %s", data.Error.Code, data.Error.Message)
	}

	if len(data.Candidates) == 0 {
		return &Response{Provider: "gemini"}, nil
	}

	candidate := data.Candidates[0]

	// Extract synthesis from content parts.
	var synthesis strings.Builder
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			synthesis.WriteString(part.Text)
		}
	}

	// Extract citations from grounding chunks.
	var results []Result
	if candidate.GroundingMetadata != nil {
		for _, chunk := range candidate.GroundingMetadata.GroundingChunks {
			if chunk.Web != nil && chunk.Web.URI != "" {
				results = append(results, Result{
					Title: chunk.Web.Title,
					URL:   chunk.Web.URI,
				})
			}
		}
	}

	return &Response{
		Results:   results,
		Synthesis: synthesis.String(),
		Provider:  "gemini",
	}, nil
}

// sanitizeGeminiError strips API keys from error messages.
func sanitizeGeminiError(msg, apiKey string) string {
	if apiKey != "" {
		msg = strings.ReplaceAll(msg, apiKey, "[REDACTED]")
	}
	return msg
}
