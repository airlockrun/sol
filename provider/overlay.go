package provider

// ProviderOverlay carries hand-maintained data that models.dev either
// doesn't track or doesn't publish for a given provider. It fills two gaps:
//
//  1. Extra models. models.dev doesn't currently list OpenAI's STT/TTS
//     lineup (gpt-4o-transcribe, whisper-1, tts-1, ...), Deepgram's stt
//     models, ElevenLabs' TTS voices, etc. We supplement them here so the
//     capability matrix reflects what a user actually gets when they
//     configure that provider.
//
//  2. Non-modality capabilities. Web search is a provider-level tool, not
//     a model modality. models.dev has no way to express "this provider
//     has a web_search tool", so we flag it here.
type ProviderOverlay struct {
	// DisplayName is used when the provider has no models.dev entry at all
	// (e.g. "brave"). For providers that exist in models.dev we keep the
	// upstream name.
	DisplayName string

	// ExtraModels are supplemental ModelInfo entries merged into the
	// provider's Models map after models.dev loads.
	ExtraModels []ModelInfo

	// ExtraCapabilities are capabilities not derivable from model
	// modalities. Currently only "search" — web search is a provider
	// feature, not a model one.
	ExtraCapabilities []string

	// SearchBackend is the sol/websearch client name used when this provider
	// supplies search. For LLM providers with native search, this differs
	// from the provider_id (xai→grok, google→gemini, moonshot→kimi) because
	// the search backend has its own historical name. For pure search
	// providers it's the same as the overlay key.
	//
	// Only set for providers that actually have a sol/websearch client
	// implementation — declaring a capability without a backend to serve
	// it would be a silent misconfiguration.
	SearchBackend string
}

// Overlay is the hand-maintained overlay map. Keyed by the same provider_id
// used in models.dev (and by the providers table in Airlock).
var Overlay = map[string]ProviderOverlay{
	"openai": {
		// STT/TTS lineup (whisper-1, gpt-4o-transcribe*, tts-1*) used to
		// live in ExtraModels here as a workaround for models.dev not
		// listing them. Goai now exposes them via TranscriptionModels()
		// and SpeechModels(), and AllProviders()'s goai merge synthesizes
		// proper ModelInfo entries with Kind set, so the override is
		// redundant. Keep the slot empty rather than removing the entry —
		// search backends still apply.
		ExtraCapabilities: []string{"search"},
		SearchBackend:     "openai", // web_search tool on the Responses API
	},
	"xai": {
		ExtraCapabilities: []string{"search"},
		SearchBackend:     "grok", // reuses the LLM provider's API key
	},
	"google": {
		ExtraCapabilities: []string{"search"},
		SearchBackend:     "gemini",
	},
	"moonshot": {
		ExtraCapabilities: []string{"search"},
		SearchBackend:     "kimi",
	},
	"perplexity": {
		ExtraCapabilities: []string{"search"},
		SearchBackend:     "perplexity",
	},
	"brave": {
		DisplayName:       "Brave Search",
		ExtraCapabilities: []string{"search"},
		SearchBackend:     "brave",
	},
}
