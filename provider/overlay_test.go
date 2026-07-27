package provider

import "testing"

// TestSearchBackends locks in the provider→websearch-backend mapping. The
// backend name differs from the provider id for LLM providers that kept their
// own historical search-client name (xai→grok, google→gemini, moonshotai→kimi);
// for pure search providers it matches the id.
func TestSearchBackends(t *testing.T) {
	want := map[string]string{
		"openai":     "openai",
		"xai":        "grok",
		"google":     "gemini",
		"moonshotai": "kimi",
		"perplexity": "perplexity",
		"brave":      "brave",
	}
	for id, backend := range want {
		if got := SearchBackend(id); got != backend {
			t.Errorf("SearchBackend(%q) = %q, want %q", id, got, backend)
		}
	}
	// A provider without web search returns empty.
	if got := SearchBackend("anthropic"); got != "" {
		t.Errorf("SearchBackend(anthropic) = %q, want empty", got)
	}
}

func TestReservedProviders(t *testing.T) {
	for id, entry := range reservedProviders {
		if entry.name == "" {
			t.Errorf("reservedProviders[%q] has empty display name", id)
		}
		if entry.capabilities == (CapabilitySet{}) && SearchBackend(id) == "" {
			t.Errorf("reserved provider %q has no capabilities", id)
		}
	}
}
