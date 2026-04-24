package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

const braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

// Brave freshness shortcuts: pd=past day, pw=past week, pm=past month, py=past year.
var freshnessToShortcut = map[string]string{
	"day":   "pd",
	"week":  "pw",
	"month": "pm",
	"year":  "py",
}

type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

func (c *DirectClient) searchBrave(ctx context.Context, req Request) (*Response, error) {
	params := url.Values{}
	params.Set("q", req.Query)
	params.Set("count", strconv.Itoa(req.Count))

	if req.Country != "" {
		params.Set("country", req.Country)
	}
	if req.Language != "" {
		params.Set("search_lang", req.Language)
	}
	if req.Freshness != "" {
		if shortcut, ok := freshnessToShortcut[req.Freshness]; ok {
			params.Set("freshness", shortcut)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", braveEndpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("websearch/brave: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Subscription-Token", c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("websearch/brave: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("websearch/brave: HTTP %d: %s", resp.StatusCode, body)
	}

	var data braveResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("websearch/brave: %w", err)
	}

	results := make([]Result, 0, len(data.Web.Results))
	for _, r := range data.Web.Results {
		results = append(results, Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
		})
	}

	return &Response{
		Results:  results,
		Provider: "brave",
	}, nil
}
