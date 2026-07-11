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

// TestSearchOnlyProviders ensures every search-only provider (absent from
// the source catalog) has both a display name (for the synthesized stub) and a search
// backend — declaring one without the other is a silent misconfiguration.
func TestSearchOnlyProviders(t *testing.T) {
	if _, ok := searchOnlyProviders["brave"]; !ok {
		t.Error("brave should be a search-only provider")
	}
	for id, displayName := range searchOnlyProviders {
		if displayName == "" {
			t.Errorf("searchOnlyProviders[%q] has empty display name", id)
		}
		if SearchBackend(id) == "" {
			t.Errorf("search-only provider %q has no SearchBackend", id)
		}
	}
}
