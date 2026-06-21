package provider

import "testing"

func TestParseOpenRouterEmbeddingModels(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"openai/text-embedding-3-small","name":"OpenAI: Text Embedding 3 Small","context_length":8191,"pricing":{"prompt":"0.00000002"}},
		{"id":"thenlper/gte-base","name":"Thenlper: GTE-Base","context_length":512,"pricing":{"prompt":"0"}},
		{"id":"","name":"no id — skipped"}
	]}`)

	models, err := parseOpenRouterEmbeddingModels(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2 (empty id dropped)", len(models))
	}

	byID := map[string]ModelInfo{}
	for _, m := range models {
		if m.Kind != KindEmbedding {
			t.Errorf("%s: kind = %q, want embedding", m.ID, m.Kind)
		}
		byID[m.ID] = m
	}

	// gte-base has no "embed" in its name — the dedicated endpoint is the only
	// way to classify it, which is the whole point.
	if _, ok := byID["thenlper/gte-base"]; !ok {
		t.Fatal("expected thenlper/gte-base in parsed models")
	}

	// Pricing: OpenRouter is per-token; we store per-million (models.dev style).
	small := byID["openai/text-embedding-3-small"]
	if small.Cost == nil {
		t.Fatal("text-embedding-3-small: expected cost")
	}
	if got := small.Cost.Input; got < 0.0199 || got > 0.0201 {
		t.Errorf("cost.Input = %v, want ~0.02 (0.00000002 * 1e6)", got)
	}
	if small.Limit == nil || small.Limit.Context != 8191 {
		t.Errorf("text-embedding-3-small: context limit not mapped: %+v", small.Limit)
	}
	// Zero price → no cost set.
	if byID["thenlper/gte-base"].Cost != nil {
		t.Error("gte-base: zero price should leave Cost nil")
	}
}

func TestMergeOpenRouterEmbeddings(t *testing.T) {
	base := &ModelsDevProvider{
		ID:   "openrouter",
		Name: "OpenRouter",
		Models: map[string]ModelInfo{
			"openai/gpt-4o": {ID: "openai/gpt-4o", Kind: KindLanguage},
		},
	}
	out := map[string]*ModelsDevProvider{"openrouter": base}

	mergeOpenRouterEmbeddings(out, []ModelInfo{
		{ID: "thenlper/gte-base", Kind: KindEmbedding},
		{ID: "openai/gpt-4o", Kind: KindEmbedding}, // collision — must NOT overwrite
	})

	got := out["openrouter"]
	if got == base {
		t.Error("expected a cloned provider, not the cached base pointer")
	}
	if got.Models["thenlper/gte-base"].Kind != KindEmbedding {
		t.Error("embedding model not merged in")
	}
	if got.Models["openai/gpt-4o"].Kind != KindLanguage {
		t.Error("additive merge overwrote an existing model")
	}
	// Cache pointer untouched.
	if len(base.Models) != 1 {
		t.Errorf("base provider mutated: %d models", len(base.Models))
	}

	// nil/empty list is a no-op.
	out2 := map[string]*ModelsDevProvider{}
	mergeOpenRouterEmbeddings(out2, nil)
	if len(out2) != 0 {
		t.Error("empty merge should be a no-op")
	}
}
