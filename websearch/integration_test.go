package websearch

import (
	"context"
	"os"
	"testing"
)

// Integration tests require real API keys and are skipped by default.
// Run with: BRAVE_API_KEY=xxx go test -v -run TestIntegration ./websearch/

func TestIntegrationBrave(t *testing.T) {
	key := os.Getenv("BRAVE_API_KEY")
	if key == "" {
		t.Skip("BRAVE_API_KEY not set")
	}

	client := NewClient(Options{Provider: "brave", APIKey: key})
	resp, err := client.Search(context.Background(), Request{Query: "Go programming language", Count: 3})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if resp.Provider != "brave" {
		t.Errorf("expected provider brave, got %s", resp.Provider)
	}
	if len(resp.Results) == 0 {
		t.Error("expected at least one result")
	}
	if resp.Synthesis != "" {
		t.Error("brave should not return synthesis")
	}
	for i, r := range resp.Results {
		t.Logf("result %d: %s — %s", i+1, r.Title, r.URL)
	}
}

func TestIntegrationPerplexity(t *testing.T) {
	key := os.Getenv("PERPLEXITY_API_KEY")
	if key == "" {
		t.Skip("PERPLEXITY_API_KEY not set")
	}

	client := NewClient(Options{Provider: "perplexity", APIKey: key})
	resp, err := client.Search(context.Background(), Request{Query: "Go programming language", Count: 3})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if resp.Provider != "perplexity" {
		t.Errorf("expected provider perplexity, got %s", resp.Provider)
	}
	if len(resp.Results) == 0 {
		t.Error("expected at least one result")
	}
	for i, r := range resp.Results {
		t.Logf("result %d: %s — %s", i+1, r.Title, r.URL)
	}
}

func TestIntegrationGrok(t *testing.T) {
	key := os.Getenv("XAI_API_KEY")
	if key == "" {
		t.Skip("XAI_API_KEY not set")
	}

	client := NewClient(Options{Provider: "grok", APIKey: key})
	resp, err := client.Search(context.Background(), Request{Query: "Go programming language"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if resp.Provider != "grok" {
		t.Errorf("expected provider grok, got %s", resp.Provider)
	}
	if resp.Synthesis == "" {
		t.Error("grok should return synthesis")
	}
	t.Logf("synthesis: %.200s...", resp.Synthesis)
	for i, r := range resp.Results {
		t.Logf("citation %d: %s", i+1, r.URL)
	}
}

func TestIntegrationGemini(t *testing.T) {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	client := NewClient(Options{Provider: "gemini", APIKey: key})
	resp, err := client.Search(context.Background(), Request{Query: "Go programming language"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if resp.Provider != "gemini" {
		t.Errorf("expected provider gemini, got %s", resp.Provider)
	}
	if resp.Synthesis == "" {
		t.Error("gemini should return synthesis")
	}
	t.Logf("synthesis: %.200s...", resp.Synthesis)
	for i, r := range resp.Results {
		t.Logf("citation %d: %s — %s", i+1, r.Title, r.URL)
	}
}

func TestIntegrationKimi(t *testing.T) {
	key := os.Getenv("KIMI_API_KEY")
	if key == "" {
		key = os.Getenv("MOONSHOT_API_KEY")
	}
	if key == "" {
		t.Skip("KIMI_API_KEY/MOONSHOT_API_KEY not set")
	}

	client := NewClient(Options{Provider: "kimi", APIKey: key})
	resp, err := client.Search(context.Background(), Request{Query: "Go programming language"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if resp.Provider != "kimi" {
		t.Errorf("expected provider kimi, got %s", resp.Provider)
	}
	if resp.Synthesis == "" {
		t.Error("kimi should return synthesis")
	}
	t.Logf("synthesis: %.200s...", resp.Synthesis)
	for i, r := range resp.Results {
		t.Logf("citation %d: %s — %s", i+1, r.Title, r.URL)
	}
}
