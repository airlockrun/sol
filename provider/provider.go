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
	"github.com/airlockrun/goai/provider/baseten"
	"github.com/airlockrun/goai/provider/cerebras"
	"github.com/airlockrun/goai/provider/cohere"
	"github.com/airlockrun/goai/provider/deepgram"
	"github.com/airlockrun/goai/provider/deepinfra"
	"github.com/airlockrun/goai/provider/deepseek"
	"github.com/airlockrun/goai/provider/elevenlabs"
	"github.com/airlockrun/goai/provider/fal"
	"github.com/airlockrun/goai/provider/fireworks"
	"github.com/airlockrun/goai/provider/google"
	"github.com/airlockrun/goai/provider/groq"
	"github.com/airlockrun/goai/provider/huggingface"
	"github.com/airlockrun/goai/provider/hume"
	"github.com/airlockrun/goai/provider/lmnt"
	"github.com/airlockrun/goai/provider/luma"
	"github.com/airlockrun/goai/provider/mistral"
	"github.com/airlockrun/goai/provider/openai"
	"github.com/airlockrun/goai/provider/perplexity"
	"github.com/airlockrun/goai/provider/proxy"
	"github.com/airlockrun/goai/provider/replicate"
	"github.com/airlockrun/goai/provider/revai"
	"github.com/airlockrun/goai/provider/togetherai"
	"github.com/airlockrun/goai/provider/xai"
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

// providerFactory builds a goai provider from sol's generic Options.
type providerFactory func(Options) provider.Provider

// providerFactories registers each goai provider sol can dispatch by ID.
// Mirrors ai-sdk's createProviderRegistry pattern (the registry itself
// is the dispatch surface; each provider package stays decoupled).
// Adapted for Go: the table is populated at init time rather than
// hand-rolled by each caller.
//
// IMPORTANT: every provider that has a dedicated goai package must be
// registered here. Falling through to the default would route the call
// to OpenAI's API with the wrong API key — and skip provider-specific
// message conversion (e.g. DeepSeek's reasoning_content rules).
var providerFactories = map[string]providerFactory{
	// Direct providers (each has its own wire format).
	"openai":    func(o Options) provider.Provider { return openai.New(provider.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"anthropic": func(o Options) provider.Provider { return anthropic.New(anthropic.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"google":    func(o Options) provider.Provider { return google.New(google.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"mistral":   func(o Options) provider.Provider { return mistral.New(mistral.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"cohere":    func(o Options) provider.Provider { return cohere.New(cohere.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },

	// OpenAI-compatible providers — must NOT fall through to the openai
	// package because their endpoints, default base URLs, and (for
	// deepseek) message conversion are all different.
	"deepseek":    func(o Options) provider.Provider { return deepseek.New(deepseek.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"groq":        func(o Options) provider.Provider { return groq.New(groq.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"fireworks":   func(o Options) provider.Provider { return fireworks.New(fireworks.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"cerebras":    func(o Options) provider.Provider { return cerebras.New(cerebras.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"perplexity":  func(o Options) provider.Provider { return perplexity.New(perplexity.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"togetherai":  func(o Options) provider.Provider { return togetherai.New(togetherai.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"deepinfra":   func(o Options) provider.Provider { return deepinfra.New(deepinfra.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"baseten":     func(o Options) provider.Provider { return baseten.New(baseten.Options{APIKey: o.APIKey}) }, // baseten.Options has no BaseURL — model deployments embed the URL
	"xai":         func(o Options) provider.Provider { return xai.New(xai.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"huggingface": func(o Options) provider.Provider { return huggingface.New(huggingface.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },

	// Speech / audio / image providers.
	"elevenlabs": func(o Options) provider.Provider { return elevenlabs.New(elevenlabs.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"deepgram":   func(o Options) provider.Provider { return deepgram.New(deepgram.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"assemblyai": func(o Options) provider.Provider { return assemblyai.New(assemblyai.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"revai":      func(o Options) provider.Provider { return revai.New(revai.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"lmnt":       func(o Options) provider.Provider { return lmnt.New(lmnt.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"hume":       func(o Options) provider.Provider { return hume.New(hume.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"fal":        func(o Options) provider.Provider { return fal.New(fal.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"luma":       func(o Options) provider.Provider { return luma.New(luma.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
	"replicate":  func(o Options) provider.Provider { return replicate.New(replicate.Options{APIKey: o.APIKey, BaseURL: o.BaseURL}) },
}

// createProvider instantiates a goai provider by ID. Unknown providers
// fall back to the openai package — preserves backward compatibility,
// but providers in providerFactories above are dispatched correctly.
func createProvider(providerID string, opts Options) provider.Provider {
	if f, ok := providerFactories[providerID]; ok {
		return f(opts)
	}
	return openai.New(provider.Options{APIKey: opts.APIKey, BaseURL: opts.BaseURL})
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
