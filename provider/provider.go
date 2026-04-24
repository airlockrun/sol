// Package provider implements the multi-provider system for sol.
// This matches opencode's provider architecture for supporting multiple LLM providers.
// Provider information is fetched dynamically from models.dev API.
package provider

import (
	"strings"

	"github.com/airlockrun/goai/model"
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
	"github.com/airlockrun/goai/provider/proxy"
	"github.com/airlockrun/goai/provider/replicate"
	"github.com/airlockrun/goai/provider/revai"
	"github.com/airlockrun/goai/stream"
)

// ParseModel parses a "provider/model" string into provider and model IDs.
// This matches opencode's parseModel function.
// Examples:
//   - "openai/gpt-4o-mini" → ("openai", "gpt-4o-mini")
//   - "anthropic/claude-3-5-sonnet" → ("anthropic", "claude-3-5-sonnet")
//   - "gpt-4o-mini" → ("openai", "gpt-4o-mini")  // defaults to openai
func ParseModel(model string) (providerID, modelID string) {
	parts := strings.SplitN(model, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	// Default to openai if no provider specified
	return "openai", model
}

// Options holds configuration for creating a provider.
type Options struct {
	APIKey  string
	BaseURL string
}

// ProxyOptions holds configuration for creating a proxy-backed model.
type ProxyOptions struct {
	BaseURL string // Proxy server URL (e.g., "http://localhost:8080")
	Token   string // Bearer token for authentication
}

// CreateModel creates a language model from provider and model IDs.
// This is the main entry point for getting a model instance.
func CreateModel(providerID, modelID string, opts Options) stream.Model {
	return createProvider(providerID, opts).Model(modelID)
}

// CreateProxyModel creates a model that proxies LLM calls through an Airlock-compatible endpoint.
// The full model string (e.g., "anthropic/claude-sonnet-4-20250514") is passed to the proxy
// which resolves credentials and forwards to the real provider.
func CreateProxyModel(fullModelID string, opts ProxyOptions) stream.Model {
	return proxy.Model(fullModelID, proxy.Options{
		BaseURL: opts.BaseURL,
		Token:   opts.Token,
	})
}

// createProvider instantiates a goai provider by ID.
func createProvider(providerID string, opts Options) provider.Provider {
	switch providerID {
	case "anthropic":
		return anthropic.New(anthropic.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "google":
		return google.New(google.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "mistral":
		return mistral.New(mistral.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "cohere":
		return cohere.New(cohere.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "elevenlabs":
		return elevenlabs.New(elevenlabs.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "deepgram":
		return deepgram.New(deepgram.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "assemblyai":
		return assemblyai.New(assemblyai.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "revai":
		return revai.New(revai.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "lmnt":
		return lmnt.New(lmnt.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "hume":
		return hume.New(hume.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "fal":
		return fal.New(fal.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "luma":
		return luma.New(luma.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	case "replicate":
		return replicate.New(replicate.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	default: // "openai" and all openai-compatible providers (groq, together, fireworks, deepseek, xai, cerebras, perplexity, etc.)
		return openai.New(provider.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
	}
}

// CreateImageModel creates an image generation model from provider and model IDs.
func CreateImageModel(providerID, modelID string, opts Options) model.ImageModel {
	return createProvider(providerID, opts).ImageModel(modelID)
}

// CreateEmbeddingModel creates an embedding model from provider and model IDs.
func CreateEmbeddingModel(providerID, modelID string, opts Options) model.EmbeddingModel {
	return createProvider(providerID, opts).EmbeddingModel(modelID)
}

// CreateSpeechModel creates a text-to-speech model from provider and model IDs.
func CreateSpeechModel(providerID, modelID string, opts Options) model.SpeechModel {
	return createProvider(providerID, opts).SpeechModel(modelID)
}

// CreateTranscriptionModel creates a speech-to-text model from provider and model IDs.
func CreateTranscriptionModel(providerID, modelID string, opts Options) model.TranscriptionModel {
	return createProvider(providerID, opts).TranscriptionModel(modelID)
}

// GetEnvVarName returns the primary environment variable name for a provider's API key.
// Uses dynamic data from models.dev when available.
func GetEnvVarName(providerID string) string {
	if info, ok := GetProviderInfo(providerID); ok && len(info.Env) > 0 {
		return info.Env[0]
	}
	// Default to OpenAI-style naming
	return strings.ToUpper(providerID) + "_API_KEY"
}

// GetDisplayName returns the display name for a provider.
// Uses dynamic data from models.dev when available.
func GetDisplayName(providerID string) string {
	if info, ok := GetProviderInfo(providerID); ok {
		return info.Name
	}
	return providerID
}
