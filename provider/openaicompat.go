package provider

import (
	"github.com/airlockrun/goai/model"
	"github.com/airlockrun/goai/provider"
	"github.com/airlockrun/goai/provider/openai"
	"github.com/airlockrun/goai/provider/openaicompat"
	"github.com/airlockrun/goai/stream"
)

// OpenRouterBaseURL is OpenRouter's OpenAI-compatible API root. Used as the
// default when a configured provider leaves the base URL blank.
const OpenRouterBaseURL = "https://openrouter.ai/api/v1"

// openAICompatProvider adapts an OpenAI-compatible endpoint that has no
// dedicated goai package (gateways like OpenRouter, or any provider the catalog
// describes but sol doesn't special-case) to the full provider.Provider
// interface.
//
// Text generation routes through openaicompat (POST {base}/chat/completions) —
// NOT the embedded modality provider, whose default Model() speaks the Responses
// API (/responses), which these endpoints don't implement. The embedded
// provider supplies the other modalities (embeddings, image, speech,
// transcription) at the SAME base URL; it defaults to the OpenAI provider (whose
// wire formats are OpenAI-compatible), but a gateway with divergent shapes —
// OpenRouter's /images and JSON-base64 transcription — passes its own.
type openAICompatProvider struct {
	provider.Provider // modality provider at the same base URL — non-text modalities.
	id                string
	compat            *openaicompat.Provider
}

// newOpenAICompatProvider builds a compat provider for id at baseURL. baseURL
// must include the version path (e.g. ".../v1"); openaicompat appends
// "/chat/completions". modalities supplies non-text models; pass nil to default
// to the OpenAI provider at the same base URL.
func newOpenAICompatProvider(id, baseURL, apiKey string, modalities provider.Provider) *openAICompatProvider {
	if modalities == nil {
		modalities = openai.New(provider.Options{APIKey: apiKey, BaseURL: baseURL})
	}
	return &openAICompatProvider{
		Provider: modalities,
		id:       id,
		compat: openaicompat.New(openaicompat.Options{
			ProviderID:                id,
			BaseURL:                   baseURL,
			APIKey:                    apiKey,
			SupportsStructuredOutputs: true,
		}),
	}
}

func (p *openAICompatProvider) ID() string { return p.id }

func (p *openAICompatProvider) Model(modelID string) stream.Model {
	return p.compat.Model(modelID)
}

func (p *openAICompatProvider) LanguageModel(modelID string) model.LanguageModel {
	return p.compat.Model(modelID)
}

var _ provider.Provider = (*openAICompatProvider)(nil)
