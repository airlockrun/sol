package provider

import "testing"

// TestAllProvidersMergesOverlay runs against whatever LoadProviders() returns
// (cached models.dev data or the built-in fallback). It asserts that the
// overlay is applied on top: openai gains its STT/TTS models, brave shows up
// as a synthetic catalog-only entry, and the shared-cache pointer is cloned
// so we don't corrupt upstream data.
func TestAllProvidersMergesOverlay(t *testing.T) {
	all, err := AllProviders()
	if err != nil {
		t.Fatalf("AllProviders: %v", err)
	}

	// Brave is a synthetic catalog-only entry.
	brave, ok := all["brave"]
	if !ok {
		t.Fatal("AllProviders missing brave (should be synthesized from overlay)")
	}
	if brave.Name != "Brave Search" {
		t.Errorf("brave.Name = %q, want Brave Search", brave.Name)
	}
	if len(brave.Models) != 0 {
		t.Errorf("brave.Models = %d entries, want 0", len(brave.Models))
	}

	// OpenAI should gain the overlay STT/TTS models on top of whatever
	// models.dev provides.
	openai, ok := all["openai"]
	if !ok {
		t.Skip("openai not in upstream provider list, skipping merge check")
	}
	for _, id := range []string{"gpt-4o-transcribe", "whisper-1", "tts-1", "tts-1-hd"} {
		if _, ok := openai.Models[id]; !ok {
			t.Errorf("openai.Models missing overlay entry %q", id)
		}
	}

	// The merged provider must not be the same pointer as the shared cache.
	// (Otherwise repeated AllProviders() calls would accumulate overlay
	// entries in the cache on every call.)
	rawBase, err := LoadProviders()
	if err != nil {
		t.Fatalf("LoadProviders: %v", err)
	}
	if rawOpenAI, ok := rawBase["openai"]; ok && rawOpenAI == openai {
		t.Error("AllProviders() returned shared cache pointer for openai — overlay merge must clone")
	}

	// Capability union: openai should have Transcription + Speech (from
	// goai-supplied gpt-4o-transcribe / tts-1 entries that AllProviders
	// synthesizes) and Search (from overlay ExtraCapabilities → Responses
	// API web_search).
	ov := Overlay["openai"]
	caps := ProviderCapabilities(openai, ov.ExtraCapabilities)
	if !caps.Transcription {
		t.Error("post-merge openai should have Transcription capability (from gpt-4o-transcribe / whisper-1)")
	}
	if !caps.Speech {
		t.Error("post-merge openai should have Speech capability (from tts-1)")
	}
	if !caps.Search {
		t.Error("post-merge openai should have Search capability (overlay Responses API web_search)")
	}
}
