package websearch

import (
	"testing"

	"github.com/airlockrun/sol/provider"
)

// TestPickSearchModel_Live asserts the autopicker resolves a current default
// web-search model for every LLM backend against the active catalog.
// It FAILS if a backend's filters (language + tool_call + non-deprecated +
// tier token) leave nothing pickable — the regression we want to catch when a
// provider deprecates or renames the model the backend used to hardcode.
//
// Skipped in -short mode and when the catalog is unavailable (offline →
// LoadProviders falls back to the model-less builtin set).
func TestPickSearchModel_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live catalog autopick in -short mode")
	}
	cat, err := provider.AllProviders()
	if err != nil {
		t.Skipf("catalog unavailable: %v", err)
	}
	if op := cat["openai"]; op == nil || len(op.Models) == 0 {
		t.Skip("catalog unavailable")
	}

	cases := []struct {
		name       string
		providerID string
		tokens     []string
	}{
		{"openai", "openai", []string{"nano", "mini"}},
		{"gemini", "google", []string{"flash-lite", "flash"}},
		{"grok", "xai", []string{"fast", "mini", "non-reasoning"}},
		{"kimi", "moonshotai", []string{"k2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Skip providers the catalog doesn't carry (offline/region) or that
			// have no tier tokens (they rely on the pinned constant by design).
			if p := cat[tc.providerID]; p == nil || len(p.Models) == 0 {
				t.Skipf("provider %q absent from catalog — relies on pinned constant", tc.providerID)
			}
			if len(tc.tokens) == 0 {
				t.Skipf("provider %q has no tier tokens — relies on pinned constant", tc.providerID)
			}
			// A populated, tier-tokened provider MUST resolve via a tier match.
			// "" here means no model id matched any token — a renamed/removed
			// tier. Fix the tokens (don't let it silently fall to a constant).
			got := pickSearchModel(tc.providerID, tc.tokens)
			if got == "" {
				t.Fatalf("pickSearchModel(%q, %v): no model matched the search tier — "+
					"the provider likely renamed/removed its small-model tier; update the tokens", tc.providerID, tc.tokens)
			}
			t.Logf("%s → %s", tc.name, got)
		})
	}
}
