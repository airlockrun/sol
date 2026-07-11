package provider

// ModelKind classifies a model's primary purpose as published by the catalog.
type ModelKind string

const (
	KindLanguage      ModelKind = "language"
	KindEmbedding     ModelKind = "embedding"
	KindImage         ModelKind = "image"
	KindAudio         ModelKind = "audio"
	KindVideo         ModelKind = "video"
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
// offers. Derived from catalog modalities, model kind, and the
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
// its kind plus its catalog modality list.
// Search is never set at the model level — it's a provider capability (see
// ProviderCapabilities).
//
// Kind-derived capabilities trust the catalog. Modalities add orthogonal
// capabilities to language models, such as vision and image output.
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
	inImage := containsStr(m.Modalities.Input, "image")
	outImage := containsStr(m.Modalities.Output, "image")
	if m.Kind == KindLanguage && inImage {
		cs.Vision = true
	}
	if m.Kind == KindLanguage && outImage {
		cs.ImageGen = true
	}
	return cs
}

// ProviderCapabilities unions per-model capabilities across every model in the
// provider, then ORs in "search" when the provider has a web-search backend
// (SearchBackend). Search is a provider feature, not derivable from any single
// model's modalities, so it's the one capability sourced outside the models.
//
// A provider with no models is valid — e.g. brave, a search-only stub — in
// which case the result is just its search capability.
func ProviderCapabilities(p *ModelsDevProvider) CapabilitySet {
	var cs CapabilitySet
	if p == nil {
		return cs
	}
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
	if SearchBackend(p.ID) != "" {
		cs.Search = true
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
