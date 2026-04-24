// Package webfetch provides URL fetching with HTML-to-markdown conversion.
package webfetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	MaxResponseSize = 5 * 1024 * 1024 // 5MB
	DefaultTimeout  = 30 * time.Second
	MaxTimeout      = 120 * time.Second
	UserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
)

// Request is the input for a web fetch operation.
type Request struct {
	URL     string `json:"url"`
	Format  string `json:"format,omitempty"`  // "markdown" (default), "text", "html"
	Timeout int    `json:"timeout,omitempty"` // seconds, max 120
}

// Response is the result of a web fetch operation.
type Response struct {
	Content     string `json:"content"`
	ContentType string `json:"contentType"`
	URL         string `json:"url"`
	StatusCode  int    `json:"statusCode"`
}

// Client fetches URLs and converts content.
type Client interface {
	Fetch(ctx context.Context, req Request) (*Response, error)
}

// DirectClient fetches URLs directly via HTTP.
type DirectClient struct{}

// NewClient creates a new direct fetch client.
func NewClient() Client {
	return &DirectClient{}
}

func (c *DirectClient) Fetch(ctx context.Context, req Request) (*Response, error) {
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		return nil, fmt.Errorf("URL must start with http:// or https://")
	}

	timeout := DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second
		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
	}

	format := req.Format
	if format == "" {
		format = "markdown"
	}

	acceptHeader := acceptForFormat(format)

	httpClient := &http.Client{Timeout: timeout}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", req.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("User-Agent", UserAgent)
	httpReq.Header.Set("Accept", acceptHeader)
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetch URL: %w", err)
	}
	defer resp.Body.Close()

	// Retry with honest UA if Cloudflare blocks.
	if resp.StatusCode == 403 && resp.Header.Get("cf-mitigated") == "challenge" {
		resp.Body.Close()
		httpReq.Header.Set("User-Agent", "opencode")
		resp, err = httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("fetch URL (retry): %w", err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != 200 {
		return &Response{
			URL:        req.URL,
			StatusCode: resp.StatusCode,
			Content:    fmt.Sprintf("Request failed with status code: %d", resp.StatusCode),
		}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > MaxResponseSize {
		return nil, fmt.Errorf("response too large (exceeds 5MB limit)")
	}

	content := string(body)
	contentType := resp.Header.Get("Content-Type")

	switch format {
	case "markdown":
		if strings.Contains(contentType, "text/html") {
			content = ConvertHTMLToMarkdown(content)
		}
	case "text":
		if strings.Contains(contentType, "text/html") {
			content = ExtractTextFromHTML(content)
		}
	}

	return &Response{
		Content:     content,
		ContentType: contentType,
		URL:         req.URL,
		StatusCode:  resp.StatusCode,
	}, nil
}

func acceptForFormat(format string) string {
	switch format {
	case "markdown":
		return "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		return "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		return "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	default:
		return "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	}
}
