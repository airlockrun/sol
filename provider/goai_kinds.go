package provider

import (
	"github.com/airlockrun/goai/provider"
	"github.com/airlockrun/goai/provider/anthropic"
	"github.com/airlockrun/goai/provider/assemblyai"
	"github.com/airlockrun/goai/provider/cohere"
	"github.com/airlockrun/goai/provider/deepgram"
	"github.com/airlockrun/goai/provider/elevenlabs"
	"github.com/airlockrun/goai/provider/fal"
	"github.com/airlockrun/goai/provider/google"
	"github.com/airlockrun/goai/provider/hume"
	"github.com/airlockrun/goai/provider/lmnt"
	"github.com/airlockrun/goai/provider/luma"
	"github.com/airlockrun/goai/provider/mistral"
	"github.com/airlockrun/goai/provider/openai"
	"github.com/airlockrun/goai/provider/replicate"
	"github.com/airlockrun/goai/provider/revai"
)

// primaryKindByProvider declares what kind of model each provider's
// goai Provider.Models() lists. The Provider interface only requires
// Models() (a generic listing) — for providers like elevenlabs (TTS),
// deepgram (STT), or fal (image), Models() returns non-language IDs.
// Providers absent from this map default to KindLanguage.
var primaryKindByProvider = map[string]ModelKind{
	"elevenlabs": KindSpeech,
	"hume":       KindSpeech,
	"lmnt":       KindSpeech,
	"assemblyai": KindTranscription,
	"deepgram":   KindTranscription, // also has SpeechModels() for TTS
	"revai":      KindTranscription,
	"fal":        KindImage,
	"luma":       KindImage,
	"replicate":  KindImage,
}

// goaiProviderFactories produces an unconfigured goai Provider per ID for
// kind enumeration. Empty Options is fine — typed-list methods are static
// and don't touch the network. Providers not listed here (groq, xai,
// cerebras, fireworks, deepseek, perplexity, togetherai, baseten,
// blackforestlabs, prodia, gladia, bedrock, azure, vertex*, etc.) are
// skipped: they're either openai-compatible (groq/xai/cerebras/etc. — sol
// reaches them via openai's transport at runtime) or don't yet need
// per-kind typing in the catalog. Add an entry here when one does.
//
// Distinct from createProvider in provider.go (which falls back to openai
// for any unknown ID — fine for runtime LLM calls but unsafe here, since
// it would stamp openai's typed lists onto e.g. groq).
var goaiProviderFactories = map[string]func() provider.Provider{
	"openai":     func() provider.Provider { return openai.New(provider.Options{}) },
	"anthropic":  func() provider.Provider { return anthropic.New(anthropic.Options{}) },
	"google":     func() provider.Provider { return google.New(google.Options{}) },
	"mistral":    func() provider.Provider { return mistral.New(mistral.Options{}) },
	"cohere":     func() provider.Provider { return cohere.New(cohere.Options{}) },
	"elevenlabs": func() provider.Provider { return elevenlabs.New(elevenlabs.Options{}) },
	"deepgram":   func() provider.Provider { return deepgram.New(deepgram.Options{}) },
	"assemblyai": func() provider.Provider { return assemblyai.New(assemblyai.Options{}) },
	"revai":      func() provider.Provider { return revai.New(revai.Options{}) },
	"lmnt":       func() provider.Provider { return lmnt.New(lmnt.Options{}) },
	"hume":       func() provider.Provider { return hume.New(hume.Options{}) },
	"fal":        func() provider.Provider { return fal.New(fal.Options{}) },
	"luma":       func() provider.Provider { return luma.New(luma.Options{}) },
	"replicate":  func() provider.Provider { return replicate.New(replicate.Options{}) },
}

// Optional typed-list interfaces. Goai providers implement these as
// concrete struct methods on a per-provider basis (openai has all five,
// cohere has Embedding+Reranking, mistral has Embedding only, etc.).
// Defining them locally lets us interface-assert without adding the
// methods to goai's Provider interface.
type embeddingLister interface {
	EmbeddingModels() []string
}

type imageLister interface {
	ImageModels() []string
}

type speechLister interface {
	SpeechModels() []string
}

type transcriptionLister interface {
	TranscriptionModels() []string
}

type rerankingLister interface {
	RerankingModels() []string
}

// goaiKindLists returns the model IDs goai knows about for a provider,
// keyed by kind. Returns nil if the provider isn't built into goai's
// typed catalog (custom or openai-compat providers fall in this bucket
// and keep modality-derived classification only).
//
// The provider's own Models() output is bucketed under
// primaryKindByProvider (defaulting to KindLanguage). Optional typed
// listers add the secondary kinds. Empty slices are dropped so callers
// can range without checks.
func goaiKindLists(providerID string) map[ModelKind][]string {
	factory, ok := goaiProviderFactories[providerID]
	if !ok {
		return nil
	}
	p := factory()
	out := map[ModelKind][]string{}

	primary := primaryKindByProvider[providerID]
	if primary == "" {
		primary = KindLanguage
	}
	if models := p.Models(); len(models) > 0 {
		out[primary] = append(out[primary], models...)
	}

	if l, ok := p.(embeddingLister); ok {
		if ids := l.EmbeddingModels(); len(ids) > 0 {
			out[KindEmbedding] = append(out[KindEmbedding], ids...)
		}
	}
	if l, ok := p.(imageLister); ok {
		if ids := l.ImageModels(); len(ids) > 0 {
			out[KindImage] = append(out[KindImage], ids...)
		}
	}
	if l, ok := p.(speechLister); ok {
		if ids := l.SpeechModels(); len(ids) > 0 {
			out[KindSpeech] = append(out[KindSpeech], ids...)
		}
	}
	if l, ok := p.(transcriptionLister); ok {
		if ids := l.TranscriptionModels(); len(ids) > 0 {
			out[KindTranscription] = append(out[KindTranscription], ids...)
		}
	}
	if l, ok := p.(rerankingLister); ok {
		if ids := l.RerankingModels(); len(ids) > 0 {
			out[KindReranking] = append(out[KindReranking], ids...)
		}
	}

	return out
}

// modalitiesForKind returns sane default modalities for a goai-listed model
// that models.dev doesn't ship. Used by the catalog merge to synthesize a
// usable ModelInfo so pickers can still pick it. Reflects the typical
// shape per kind; if a provider's own models.dev entry is more specific,
// the merge keeps that and only stamps Kind.
func modalitiesForKind(k ModelKind) *ModelModalities {
	switch k {
	case KindLanguage:
		return &ModelModalities{Input: []string{"text"}, Output: []string{"text"}}
	case KindEmbedding:
		return &ModelModalities{Input: []string{"text"}, Output: []string{}}
	case KindImage:
		return &ModelModalities{Input: []string{"text"}, Output: []string{"image"}}
	case KindSpeech:
		return &ModelModalities{Input: []string{"text"}, Output: []string{"audio"}}
	case KindTranscription:
		return &ModelModalities{Input: []string{"audio"}, Output: []string{"text"}}
	case KindReranking:
		return &ModelModalities{Input: []string{"text"}, Output: []string{}}
	}
	return nil
}
