// Package websearch provides a web search client with multiple provider backends.
//
// The package exposes a Client interface that can be implemented by different
// backends (direct API calls, proxy through Airlock, etc.). NewClient creates
// a direct client that calls provider APIs with explicit credentials.
package websearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	defaultCount   = 5
	maxCount       = 10
	defaultTimeout = 30 * time.Second
)

// Client searches the web.
type Client interface {
	Search(ctx context.Context, req Request) (*Response, error)
}

// Request describes a web search query.
type Request struct {
	Query     string // search query (required)
	Count     int    // number of results, 1-10; 0 = provider default (5)
	Freshness string // time filter: "day", "week", "month", "year"
	Country   string // 2-letter country code
	Language  string // ISO 639-1 language code
}

// Response contains web search results.
type Response struct {
	Results   []Result // raw results (Brave/Perplexity) or citation sources (LLM providers)
	Synthesis string   // synthesized answer; only set by LLM-based providers (Grok, Gemini, Kimi)
	Provider  string   // which provider produced these results
}

// Result is a single search result.
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Options configures a direct search client. Mirrors the LLM provider pattern:
// single provider, single key, caller reads env.
type Options struct {
	Provider string // "brave", "perplexity", "grok", "gemini", "kimi" (required)
	APIKey   string // provider's API key (required)
	Model    string // for LLM-based providers; defaults per provider
}

// DirectClient calls search provider APIs directly.
type DirectClient struct {
	provider string
	apiKey   string
	model    string
	http     *http.Client
}

// NewClient creates a direct search client from explicit options.
func NewClient(opts Options) *DirectClient {
	return &DirectClient{
		provider: opts.Provider,
		apiKey:   opts.APIKey,
		model:    opts.Model,
		http:     &http.Client{Timeout: defaultTimeout},
	}
}

// Search implements Client.
func (c *DirectClient) Search(ctx context.Context, req Request) (*Response, error) {
	if req.Query == "" {
		return nil, errors.New("websearch: query is required")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("websearch: no API key configured for provider %q", c.provider)
	}

	count := req.Count
	if count <= 0 {
		count = defaultCount
	}
	if count > maxCount {
		count = maxCount
	}
	req.Count = count

	switch c.provider {
	case "brave":
		return c.searchBrave(ctx, req)
	case "perplexity":
		return c.searchPerplexity(ctx, req)
	case "grok":
		return c.searchGrok(ctx, req)
	case "gemini":
		return c.searchGemini(ctx, req)
	case "kimi":
		return c.searchKimi(ctx, req)
	case "openai":
		return c.searchOpenAI(ctx, req)
	default:
		return nil, fmt.Errorf("websearch: unknown provider %q", c.provider)
	}
}
