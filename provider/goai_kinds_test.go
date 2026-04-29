package provider

import (
	"testing"
)

// TestGoaiKindLists_OpenAI smokes the four typed lists openai's goai
// provider exposes — these are the ones that used to require Overlay
// ExtraModels. Concrete model IDs come from the ai-sdk mirror in goai;
// asserting on a few canonical names guards against accidental list
// truncation.
func TestGoaiKindLists_OpenAI(t *testing.T) {
	kinds := goaiKindLists("openai")
	if kinds == nil {
		t.Fatal("goaiKindLists(\"openai\") returned nil")
	}

	// Primary kind = language. Spot-check a current-gen model is present.
	requireContains(t, "openai/language", kinds[KindLanguage], "gpt-5")

	// Embedding lineup.
	requireContains(t, "openai/embedding", kinds[KindEmbedding], "text-embedding-3-small")
	requireContains(t, "openai/embedding", kinds[KindEmbedding], "text-embedding-3-large")

	// Image-gen lineup.
	requireContains(t, "openai/image", kinds[KindImage], "dall-e-3")

	// Speech (TTS).
	requireContains(t, "openai/speech", kinds[KindSpeech], "tts-1")
	requireContains(t, "openai/speech", kinds[KindSpeech], "tts-1-hd")

	// Transcription (STT) — the entries that used to live in Overlay.
	requireContains(t, "openai/transcription", kinds[KindTranscription], "whisper-1")
	requireContains(t, "openai/transcription", kinds[KindTranscription], "gpt-4o-transcribe")
}

// TestGoaiKindLists_PrimaryKindOverrides verifies the primaryKindByProvider
// map: providers whose Models() lists non-language IDs (elevenlabs is TTS,
// deepgram is STT) bucket their primary list under the right kind.
func TestGoaiKindLists_PrimaryKindOverrides(t *testing.T) {
	cases := []struct {
		provider string
		kind     ModelKind
		example  string
	}{
		{"elevenlabs", KindSpeech, "eleven_multilingual_v2"},
		{"hume", KindSpeech, "octave"},
		{"lmnt", KindSpeech, "lily"},
		{"deepgram", KindTranscription, "nova-2"},
		{"assemblyai", KindTranscription, "best"},
		{"revai", KindTranscription, "default"},
		{"fal", KindImage, "fal-ai/flux/dev"},
		{"luma", KindImage, "photon-1"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			kinds := goaiKindLists(tc.provider)
			if kinds == nil {
				t.Fatalf("goaiKindLists(%q) returned nil", tc.provider)
			}
			requireContains(t, tc.provider+"/"+string(tc.kind), kinds[tc.kind], tc.example)
		})
	}
}

// TestGoaiKindLists_UnknownProvider returns nil so the catalog merge knows
// to skip stamping for openai-compat providers (groq/xai/etc.) — calling
// createProvider() for those would return openai's lists, which are wrong
// to attribute to the compat provider.
func TestGoaiKindLists_UnknownProvider(t *testing.T) {
	if got := goaiKindLists("definitely-not-a-provider"); got != nil {
		t.Errorf("goaiKindLists(unknown) = %v, want nil", got)
	}
}

// TestAllProvidersGoaiKindMerge verifies the catalog merge actually stamps
// Kind on existing models.dev entries and synthesizes missing ones (the
// whisper-1/tts-1 case that used to live in Overlay).
func TestAllProvidersGoaiKindMerge(t *testing.T) {
	out, err := AllProviders()
	if err != nil {
		t.Fatalf("AllProviders: %v", err)
	}
	openaiProv, ok := out["openai"]
	if !ok {
		t.Fatal("AllProviders missing openai")
	}

	// Synthesized from goai (used to come from Overlay.ExtraModels).
	for _, id := range []string{"whisper-1", "tts-1"} {
		m, ok := openaiProv.Models[id]
		if !ok {
			t.Errorf("AllProviders().openai.Models[%q] missing — goai merge should synthesize", id)
			continue
		}
		// whisper-1 → transcription, tts-1 → speech. Both Kind values
		// must be set so the frontend filters correctly.
		switch id {
		case "whisper-1":
			if m.Kind != KindTranscription {
				t.Errorf("openai/%s Kind = %q, want %q", id, m.Kind, KindTranscription)
			}
		case "tts-1":
			if m.Kind != KindSpeech {
				t.Errorf("openai/%s Kind = %q, want %q", id, m.Kind, KindSpeech)
			}
		}
	}

	// Embedding models: text-embedding-3-small is in models.dev. Goai
	// stamps Kind on the existing entry without clobbering its modalities.
	if m, ok := openaiProv.Models["text-embedding-3-small"]; ok {
		if m.Kind != KindEmbedding {
			t.Errorf("openai/text-embedding-3-small Kind = %q, want %q", m.Kind, KindEmbedding)
		}
	}
	// Note: not all providers' embedding models appear in models.dev. The
	// model existing at all is the precondition; Kind stamping is what we
	// added.
}

func requireContains(t *testing.T, label string, list []string, want string) {
	t.Helper()
	for _, s := range list {
		if s == want {
			return
		}
	}
	t.Errorf("%s missing expected ID %q (have %d entries)", label, want, len(list))
}
