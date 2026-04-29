package provider

import "testing"

// TestOverlayOpenAISearch locks in the openai search-backend declaration.
// The STT/TTS ExtraModels block this test used to enforce moved to
// goai_kinds.go's typed-list merge — see TestAllProvidersGoaiKindMerge.
func TestOverlayOpenAISearch(t *testing.T) {
	ov, ok := Overlay["openai"]
	if !ok {
		t.Fatal("Overlay[\"openai\"] missing")
	}
	if len(ov.ExtraModels) != 0 {
		t.Errorf("openai overlay ExtraModels should be empty (goai supplies STT/TTS), got %d entries", len(ov.ExtraModels))
	}
	if !containsStr(ov.ExtraCapabilities, CapSearch) {
		t.Error("openai overlay should declare search (Responses API web_search tool)")
	}
	if ov.SearchBackend != "openai" {
		t.Errorf("openai overlay SearchBackend = %q, want %q", ov.SearchBackend, "openai")
	}
}

// TestOverlaySearchProviders ensures every provider declaring search also
// sets a SearchBackend — declaring search without a backend is a silent
// misconfiguration (the capability matrix would promise what the runtime
// can't deliver).
func TestOverlaySearchProviders(t *testing.T) {
	expect := []string{"openai", "xai", "google", "moonshot", "perplexity", "brave"}
	for _, id := range expect {
		ov, ok := Overlay[id]
		if !ok {
			t.Errorf("Overlay[%q] missing — expected to declare search", id)
			continue
		}
		if !containsStr(ov.ExtraCapabilities, CapSearch) {
			t.Errorf("Overlay[%q].ExtraCapabilities = %v, expected to contain %q", id, ov.ExtraCapabilities, CapSearch)
		}
		if ov.SearchBackend == "" {
			t.Errorf("Overlay[%q] declares search but has empty SearchBackend", id)
		}
	}

	// Also: any provider with ExtraCapabilities=[search] must have a
	// SearchBackend, and vice versa. Enforces the invariant.
	for id, ov := range Overlay {
		hasSearch := containsStr(ov.ExtraCapabilities, CapSearch)
		if hasSearch && ov.SearchBackend == "" {
			t.Errorf("Overlay[%q]: search capability declared but SearchBackend empty", id)
		}
		if !hasSearch && ov.SearchBackend != "" {
			t.Errorf("Overlay[%q]: SearchBackend=%q set but search capability not declared", id, ov.SearchBackend)
		}
	}
}

// TestOverlayBraveIsCatalogOnly verifies brave has a display name (since it
// isn't in models.dev) and no LLM models.
func TestOverlayBraveIsCatalogOnly(t *testing.T) {
	ov, ok := Overlay["brave"]
	if !ok {
		t.Fatal("Overlay[\"brave\"] missing")
	}
	if ov.DisplayName == "" {
		t.Error("brave overlay must set DisplayName (it has no models.dev entry)")
	}
	if len(ov.ExtraModels) != 0 {
		t.Errorf("brave overlay should have no ExtraModels, got %d", len(ov.ExtraModels))
	}
	if !containsStr(ov.ExtraCapabilities, CapSearch) {
		t.Error("brave overlay must have search capability")
	}
}
