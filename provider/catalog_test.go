package provider

import "testing"

// TestAllProvidersMergesOverlay runs against whatever LoadProviders() returns
// (cached models.dev data or the built-in fallback). It asserts the sol-owned
// enrichment on top: the search overlay (brave synthetic entry + openai's
// SearchBackend-derived capability) and that the shared cache pointer is cloned
// before any Kind is stamped.
func TestAllProvidersMergesOverlay(t *testing.T) {
	all, err := AllProviders()
	if err != nil {
		t.Fatalf("AllProviders: %v", err)
	}

	// Search overlay: brave is a synthetic catalog-only entry (models.dev has
	// no notion of search providers). This is the ONLY hand-maintained data.
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

	openai, ok := all["openai"]
	if !ok {
		t.Skip("openai not in upstream provider list, skipping enrichment check")
	}

	// Search capability derives from SearchBackend (overlay), not from any
	// model — openai serves web_search via the Responses API.
	if !ProviderCapabilities(openai).Search {
		t.Error("post-merge openai should have Search capability (SearchBackend → Responses API web_search)")
	}

	// Cache-clone safety: AllProviders stamps sol-derived Kind, but must clone
	// before mutating so LoadProviders' cache stays pristine. Find a model
	// AllProviders classified (embedding by name / image by modality) and
	// assert the cached copy still has an empty Kind.
	rawBase, err := LoadProviders()
	if err != nil {
		t.Fatalf("LoadProviders: %v", err)
	}
	rawOpenAI, ok := rawBase["openai"]
	if !ok {
		return
	}
	for id, m := range openai.Models {
		if m.Kind == "" {
			continue
		}
		if raw, ok := rawOpenAI.Models[id]; ok && raw.Kind != "" {
			t.Errorf("AllProviders mutated the LoadProviders cache: openai/%s has Kind=%q in the base map", id, raw.Kind)
		}
		if rawOpenAI == openai {
			t.Error("AllProviders returned the shared cache pointer for openai — classification must clone")
		}
		break
	}
}

func TestDerivedKind(t *testing.T) {
	img := func(out ...string) ModelInfo {
		return ModelInfo{ID: "m", Name: "m", Modalities: &ModelModalities{Output: out}}
	}
	cases := []struct {
		name string
		m    ModelInfo
		want ModelKind
	}{
		{"chat text→text", img("text"), ""},
		{"image-only output", img("image"), KindImage},
		{"text+image output is chat, not image", img("text", "image"), ""},
		{"embedding by name", ModelInfo{ID: "text-embedding-3-small", Name: "Text Embedding 3"}, KindEmbedding},
		{"embedding name beats image modality", ModelInfo{ID: "some-embed", Name: "x", Modalities: &ModelModalities{Output: []string{"image"}}}, KindEmbedding},
		{"nil modalities, plain name", ModelInfo{ID: "gpt-5", Name: "GPT-5"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := derivedKind(tc.m); got != tc.want {
				t.Errorf("derivedKind = %q, want %q", got, tc.want)
			}
		})
	}
}
