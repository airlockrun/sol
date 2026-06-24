package provider

import "testing"

func TestParseOpenRouterModels(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"openai/text-embedding-3-small","name":"Embed 3 Small","context_length":8191,"pricing":{"prompt":"0.00000002"},"architecture":{"output_modalities":["embeddings"]}},
		{"id":"thenlper/gte-base","name":"GTE-Base","context_length":512,"pricing":{"prompt":"0"},"architecture":{"output_modalities":["embeddings"]}},
		{"id":"microsoft/mai-voice-2","name":"MAI Voice","pricing":{"prompt":"0.000022"},"architecture":{"output_modalities":["speech"]}},
		{"id":"openai/whisper-1","name":"Whisper","pricing":{"prompt":"0.36"},"architecture":{"output_modalities":["transcription"]}},
		{"id":"bytedance/seedream","name":"Seedream","architecture":{"output_modalities":["image"]}},
		{"id":"openai/gpt-5","name":"GPT-5 (chat)","architecture":{"output_modalities":["text"]}},
		{"id":"openai/gpt-audio","name":"GPT Audio (chat)","architecture":{"output_modalities":["text","audio"]}},
		{"id":"","name":"no id","architecture":{"output_modalities":["embeddings"]}}
	]}`)

	models, err := parseOpenRouterModels(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byID := map[string]ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}

	// Chat (text) and chat-with-audio (text+audio) are NOT classified here:
	// models.dev owns chat, and gpt-audio is a chat model — not a TTS/STT slot
	// model (its audio is via /chat/completions, not /audio/*). Empty id dropped.
	for _, id := range []string{"openai/gpt-5", "openai/gpt-audio", ""} {
		if _, ok := byID[id]; ok {
			t.Errorf("%q should be skipped (chat/empty), not classified", id)
		}
	}

	wantKind := map[string]ModelKind{
		"openai/text-embedding-3-small": KindEmbedding,
		"thenlper/gte-base":             KindEmbedding,
		"microsoft/mai-voice-2":         KindSpeech,
		"openai/whisper-1":              KindTranscription,
		"bytedance/seedream":            KindImage,
	}
	for id, want := range wantKind {
		m, ok := byID[id]
		if !ok {
			t.Errorf("%s: not parsed", id)
			continue
		}
		if m.Kind != want {
			t.Errorf("%s: kind = %q, want %q", id, m.Kind, want)
		}
	}

	// Cost is per million tokens, set only for per-token modalities.
	if c := byID["openai/text-embedding-3-small"].Cost; c == nil || c.Input < 0.0199 || c.Input > 0.0201 {
		t.Errorf("embedding cost = %v, want ~0.02 (0.00000002 * 1e6)", byID["openai/text-embedding-3-small"].Cost)
	}
	if byID["microsoft/mai-voice-2"].Cost == nil {
		t.Error("speech: expected per-token cost from pricing.prompt")
	}
	// Transcription bills per audio unit, image per image → pricing.prompt isn't
	// per-token, so no cost; zero-price embedding likewise.
	if byID["openai/whisper-1"].Cost != nil {
		t.Errorf("transcription cost should be nil (audio-unit pricing), got %+v", byID["openai/whisper-1"].Cost)
	}
	if byID["bytedance/seedream"].Cost != nil {
		t.Error("image cost should be nil")
	}
	if byID["thenlper/gte-base"].Cost != nil {
		t.Error("gte-base: zero price should leave Cost nil")
	}
	if l := byID["openai/text-embedding-3-small"].Limit; l == nil || l.Context != 8191 {
		t.Errorf("embedding context limit not mapped: %+v", byID["openai/text-embedding-3-small"].Limit)
	}
}

func TestMergeOpenRouterModels(t *testing.T) {
	base := &ModelsDevProvider{
		ID:   "openrouter",
		Name: "OpenRouter",
		Models: map[string]ModelInfo{
			"openai/gpt-4o": {ID: "openai/gpt-4o", Kind: KindLanguage},
		},
	}
	out := map[string]*ModelsDevProvider{"openrouter": base}

	mergeOpenRouterModels(out, []ModelInfo{
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
	if len(base.Models) != 1 {
		t.Errorf("base provider mutated: %d models", len(base.Models))
	}

	out2 := map[string]*ModelsDevProvider{}
	mergeOpenRouterModels(out2, nil)
	if len(out2) != 0 {
		t.Error("empty merge should be a no-op")
	}
}
