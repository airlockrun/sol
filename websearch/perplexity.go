package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const perplexityEndpoint = "https://api.perplexity.ai/search"

// Freshness values that Perplexity accepts as search_recency_filter.
var perplexityRecency = map[string]string{
	"day":   "day",
	"week":  "week",
	"month": "month",
	"year":  "year",
}

type perplexityRequest struct {
	Query                string   `json:"query"`
	MaxResults           int      `json:"max_results,omitempty"`
	Country              string   `json:"country,omitempty"`
	SearchRecencyFilter  string   `json:"search_recency_filter,omitempty"`
	SearchLanguageFilter []string `json:"search_language_filter,omitempty"`
}

type perplexityResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

func (c *DirectClient) searchPerplexity(ctx context.Context, req Request) (*Response, error) {
	body := perplexityRequest{
		Query:      req.Query,
		MaxResults: req.Count,
	}
	if req.Country != "" {
		body.Country = req.Country
	}
	if req.Language != "" {
		body.SearchLanguageFilter = []string{req.Language}
	}
	if req.Freshness != "" {
		if recency, ok := perplexityRecency[req.Freshness]; ok {
			body.SearchRecencyFilter = recency
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("websearch/perplexity: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", perplexityEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("websearch/perplexity: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("websearch/perplexity: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("websearch/perplexity: HTTP %d: %s", resp.StatusCode, respBody)
	}

	var data perplexityResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("websearch/perplexity: %w", err)
	}

	results := make([]Result, 0, len(data.Results))
	for _, r := range data.Results {
		results = append(results, Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Snippet,
		})
	}

	return &Response{
		Results:  results,
		Provider: "perplexity",
	}, nil
}
