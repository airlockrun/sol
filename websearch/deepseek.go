package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// https://api-docs.deepseek.com/guides/search/
const deepseekEndpoint = "https://api.deepseek.com/v1/tools/search"

func (c *DirectClient) searchDeepSeek(ctx context.Context, req Request) (*Response, error) {
	if !utf8.ValidString(req.Query) || utf8.RuneCountInString(req.Query) > 128 {
		return nil, errors.New("websearch/deepseek: query must contain 1 to 128 Unicode characters")
	}
	payload, err := json.Marshal(struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}{req.Query, req.Count})
	if err != nil {
		return nil, fmt.Errorf("websearch/deepseek: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(deepseekEndpoint, "/tools/search"), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("websearch/deepseek: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("websearch/deepseek: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("websearch/deepseek: HTTP %d: %s", resp.StatusCode, body)
	}
	var data struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Summary string `json:"summary"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("websearch/deepseek: %w", err)
	}
	results := make([]Result, 0, len(data.Results))
	for _, r := range data.Results {
		snippet := r.Summary
		if strings.TrimSpace(snippet) == "" {
			snippet = r.Snippet
		}
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: snippet})
	}
	return &Response{Results: results, Provider: "deepseek"}, nil
}
