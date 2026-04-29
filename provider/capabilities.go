package provider

// ModelKind classifies a model by its primary purpose. Sourced from goai's
// per-provider typed lists (Models / EmbeddingModels / ImageModels /
// SpeechModels / TranscriptionModels / RerankingModels). Empty means sol
// has no goai typed-list coverage for the provider — currently true for
// the openai-compat bucket (groq, xai, cerebras, fireworks, deepseek,
// perplexity, togetherai, etc.), all of which ship language models, so
// callers can safely treat empty as KindLanguage when filtering.
type ModelKind string

const (
	KindLanguage      ModelKind = "language"
	KindEmbedding     ModelKind = "embedding"
	KindImage         ModelKind = "image"
	KindSpeech        ModelKind = "speech"        // text-to-speech
	KindTranscription ModelKind = "transcription" // speech-to-text
	KindReranking     ModelKind = "reranking"
)

// Capability constants. These are the strings exposed over the API and used
// as keys in the UI capability matrix. Keep them lowercase snake_case.
//
// Two axes:
//   - Kind-derived (CapEmbedding/CapSpeech/CapTranscription/CapReranking,
//     plus CapText/CapImageGen which double as kinds). True when the
//     provider has at least one model of that kind.
//   - Modality-derived (CapVision). True when the provider has at least
//     one language model that accepts the corresponding extra input
//     modality.
const (
	CapText          = "text"
	CapVision        = "vision"
	CapImageGen      = "image_gen"
	CapSearch        = "search"
	CapEmbedding     = "embedding"
	CapSpeech        = "speech"
	CapTranscription = "transcription"
	CapReranking     = "reranking"
)

// CapabilitySet is the set of high-level capabilities a model or provider
// offers. Derived from models.dev modalities, goai-supplied kind, and the
// overlay's extras.
type CapabilitySet struct {
	Text          bool
	Vision        bool
	ImageGen      bool
	Search        bool
	Embedding     bool
	Speech        bool
	Transcription bool
	Reranking     bool
}

// List returns the set as a sorted slice of capability strings in the
// canonical UI order.
func (c CapabilitySet) List() []string {
	out := make([]string, 0, 8)
	if c.Text {
		out = append(out, CapText)
	}
	if c.Vision {
		out = append(out, CapVision)
	}
	if c.Transcription {
		out = append(out, CapTranscription)
	}
	if c.Speech {
		out = append(out, CapSpeech)
	}
	if c.ImageGen {
		out = append(out, CapImageGen)
	}
	if c.Search {
		out = append(out, CapSearch)
	}
	if c.Embedding {
		out = append(out, CapEmbedding)
	}
	if c.Reranking {
		out = append(out, CapReranking)
	}
	return out
}

// CapabilitiesFromModel derives the capability set for a single model from
// its kind (goai-sourced) plus its modality list (models.dev-sourced).
// Search is never set at the model level — it's a provider capability (see
// ProviderCapabilities).
//
// Kind-derived caps fire whenever Kind is set; modality-derived caps fire
// independently — a Kind=Language model with image input still gets Vision.
// Modality-only classification is the fallback for catalog entries from
// providers without goai typed-list coverage.
func CapabilitiesFromModel(m ModelInfo) CapabilitySet {
	var cs CapabilitySet

	switch m.Kind {
	case KindLanguage:
		cs.Text = true
	case KindEmbedding:
		cs.Embedding = true
	case KindImage:
		cs.ImageGen = true
	case KindSpeech:
		cs.Speech = true
	case KindTranscription:
		cs.Transcription = true
	case KindReranking:
		cs.Reranking = true
	}

	if m.Modalities == nil {
		return cs
	}
	inText := containsStr(m.Modalities.Input, "text")
	outText := containsStr(m.Modalities.Output, "text")
	inImage := containsStr(m.Modalities.Input, "image")
	outImage := containsStr(m.Modalities.Output, "image")
	inAudio := containsStr(m.Modalities.Input, "audio")
	outAudio := containsStr(m.Modalities.Output, "audio")

	if inText && outText {
		cs.Text = true
	}
	if inImage && outText {
		cs.Vision = true
	}
	if inAudio && outText {
		cs.Transcription = true
	}
	if inText && outAudio {
		cs.Speech = true
	}
	if inText && outImage {
		cs.ImageGen = true
	}
	return cs
}

// ProviderCapabilities unions ModelCapabilities across every model in the
// provider and then ORs in extras (capabilities that aren't derivable from
// any single model's modalities, such as "search"). Callers typically pass
// Overlay[providerID].ExtraCapabilities as extras.
//
// It's valid for a provider to have no models — e.g. brave, which is only
// represented via Overlay. In that case the result is whatever the extras
// declare.
func ProviderCapabilities(p *ModelsDevProvider, extras []string) CapabilitySet {
	var cs CapabilitySet
	if p != nil {
		for _, m := range p.Models {
			mc := CapabilitiesFromModel(m)
			cs.Text = cs.Text || mc.Text
			cs.Vision = cs.Vision || mc.Vision
			cs.ImageGen = cs.ImageGen || mc.ImageGen
			cs.Embedding = cs.Embedding || mc.Embedding
			cs.Speech = cs.Speech || mc.Speech
			cs.Transcription = cs.Transcription || mc.Transcription
			cs.Reranking = cs.Reranking || mc.Reranking
		}
	}
	for _, cap := range extras {
		switch cap {
		case CapText:
			cs.Text = true
		case CapVision:
			cs.Vision = true
		case CapImageGen:
			cs.ImageGen = true
		case CapSearch:
			cs.Search = true
		case CapEmbedding:
			cs.Embedding = true
		case CapSpeech:
			cs.Speech = true
		case CapTranscription:
			cs.Transcription = true
		case CapReranking:
			cs.Reranking = true
		}
	}
	return cs
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
