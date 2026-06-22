package provider

import "testing"

// TestCreateProvider_OpenRouterRoutesToCompat pins the OpenRouter fix: it must
// route through the OpenAI-compatible wrapper at OpenRouter's base URL (and the
// supplied key), never the openai package / api.openai.com.
func TestCreateProvider_OpenRouterRoutesToCompat(t *testing.T) {
	p := createProvider("openrouter", Options{APIKey: "sk-or-test"})
	cp, ok := p.(*openAICompatProvider)
	if !ok {
		t.Fatalf("openrouter provider type = %T, want *openAICompatProvider", p)
	}
	if cp.ID() != "openrouter" {
		t.Errorf("ID = %q, want openrouter", cp.ID())
	}
	if got := cp.compat.BaseURL(); got != OpenRouterBaseURL {
		t.Errorf("base URL = %q, want %q (default)", got, OpenRouterBaseURL)
	}
	if got := cp.compat.APIKey(); got != "sk-or-test" {
		t.Errorf("api key = %q, not propagated", got)
	}
}

// An explicit base URL (e.g. an OpenRouter-compatible proxy) overrides the
// default.
func TestCreateProvider_OpenRouterBaseURLOverride(t *testing.T) {
	p := createProvider("openrouter", Options{APIKey: "k", BaseURL: "https://proxy.test/v1"})
	cp := p.(*openAICompatProvider)
	if got := cp.compat.BaseURL(); got != "https://proxy.test/v1" {
		t.Errorf("base URL = %q, want the override", got)
	}
}

// An unregistered provider with a configured base URL is treated as
// OpenAI-compatible (chat/completions), not routed to api.openai.com.
func TestCreateProvider_UnknownIsOpenAICompat(t *testing.T) {
	p := createProvider("some-gateway", Options{APIKey: "k", BaseURL: "https://gw.test/v1"})
	cp, ok := p.(*openAICompatProvider)
	if !ok {
		t.Fatalf("unknown provider type = %T, want *openAICompatProvider", p)
	}
	if got := cp.compat.BaseURL(); got != "https://gw.test/v1" {
		t.Errorf("base URL = %q, want the configured value", got)
	}
}

// The real openai provider is unchanged — it must keep using the openai package
// (Responses API), not the compat wrapper.
func TestCreateProvider_OpenAIUnchanged(t *testing.T) {
	p := createProvider("openai", Options{APIKey: "k"})
	if _, ok := p.(*openAICompatProvider); ok {
		t.Fatal("openai must not be routed through the OpenAI-compatible wrapper")
	}
}
