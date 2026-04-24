package provider

import "testing"

// TestOverlayOpenAIExtras locks in the expected OpenAI STT/TTS entries so a
// refactor can't silently delete them.
func TestOverlayOpenAIExtras(t *testing.T) {
	ov, ok := Overlay["openai"]
	if !ok {
		t.Fatal("Overlay[\"openai\"] missing")
	}

	wantModels := map[string]CapabilitySet{
		"whisper-1":                 {STT: true},
		"gpt-4o-mini-transcribe":    {STT: true},
		"gpt-4o-transcribe":         {STT: true},
		"gpt-4o-transcribe-diarize": {STT: true},
		"tts-1":                     {TTS: true},
		"tts-1-hd":                  {TTS: true},
	}

	got := map[string]bool{}
	for _, m := range ov.ExtraModels {
		got[m.ID] = true
		want, ok := wantModels[m.ID]
		if !ok {
			t.Errorf("unexpected extra model %q in openai overlay", m.ID)
			continue
		}
		if c := CapabilitiesFromModel(m); c != want {
			t.Errorf("openai/%s capabilities = %+v, want %+v", m.ID, c, want)
		}
	}
	for id := range wantModels {
		if !got[id] {
			t.Errorf("openai overlay missing expected model %q", id)
		}
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
