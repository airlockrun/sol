// Package provider implements the multi-provider system for sol.
// This file contains transformation logic matching opencode's ProviderTransform.

package provider

import "strings"

// ModelCapabilities describes capabilities of a model.
// This matches opencode's getResponsesModelConfig() in openai-responses-language-model.ts
type ModelCapabilities struct {
	// IsReasoningModel indicates if the model is a reasoning model
	IsReasoningModel bool

	// SystemMessageMode determines how system messages should be handled.
	// "system" - use standard system role
	// "developer" - convert to developer role (for reasoning models)
	// "remove" - remove system messages entirely
	SystemMessageMode string

	// DefaultReasoningEffort is the default reasoning effort level for this model.
	// Empty string means no default (non-reasoning model or special case like gpt-5-pro).
	DefaultReasoningEffort string

	// DefaultMaxOutputTokens is the recommended max output tokens for this model.
	// 0 means use the standard default.
	DefaultMaxOutputTokens int
}

// GetModelCapabilities returns capabilities for a given model ID.
// This matches opencode's getResponsesModelConfig() function.
func GetModelCapabilities(modelID string) ModelCapabilities {
	defaults := ModelCapabilities{
		SystemMessageMode:      "system",
		IsReasoningModel:       false,
		DefaultReasoningEffort: "",
		DefaultMaxOutputTokens: 0,
	}

	// gpt-5-chat models are non-reasoning (matching opencode)
	if strings.HasPrefix(modelID, "gpt-5-chat") {
		return defaults
	}

	// o series and gpt-5 reasoning models (matching opencode's getResponsesModelConfig)
	if strings.HasPrefix(modelID, "o") ||
		strings.HasPrefix(modelID, "gpt-5") ||
		strings.HasPrefix(modelID, "codex-") ||
		strings.HasPrefix(modelID, "computer-use") {

		// o1-mini and o1-preview use "remove" mode
		if strings.HasPrefix(modelID, "o1-mini") || strings.HasPrefix(modelID, "o1-preview") {
			defaults.IsReasoningModel = true
			defaults.SystemMessageMode = "remove"
			return defaults
		}

		// All other o/gpt-5/codex/computer-use models use "developer" mode
		defaults.IsReasoningModel = true
		defaults.SystemMessageMode = "developer"

		// gpt-5 models (except gpt-5-pro) get default reasoning effort "medium"
		// and higher max output tokens (32000)
		if strings.HasPrefix(modelID, "gpt-5") && !strings.HasPrefix(modelID, "gpt-5-pro") {
			defaults.DefaultReasoningEffort = "medium"
			defaults.DefaultMaxOutputTokens = 32000
		}

		return defaults
	}

	// All other models (gpt-4, etc.) are non-reasoning
	return defaults
}

// ProviderOptions returns the provider-specific options for a model.
// This matches opencode's ProviderTransform.options() function.
func ProviderOptions(providerID, modelID, sessionID string) map[string]any {
	caps := GetModelCapabilities(modelID)
	result := make(map[string]any)

	// OpenAI provider options
	if providerID == "openai" {
		// Note: store defaults to false in goai's Responses API provider
		// for privacy. No need to set it here explicitly.

		// promptCacheKey for session caching
		result["promptCacheKey"] = sessionID

		// systemMessageMode based on model capabilities
		result["systemMessageMode"] = caps.SystemMessageMode
	}

	// gpt-5 reasoning model options (matching opencode's transform.ts options() function)
	if strings.HasPrefix(modelID, "gpt-5") && !strings.HasPrefix(modelID, "gpt-5-chat") {
		// gpt-5-pro doesn't get reasoningEffort
		if !strings.HasPrefix(modelID, "gpt-5-pro") {
			result["reasoningEffort"] = "medium"
		}

		// Include reasoning content for gpt-5 models
		result["include"] = []string{"reasoning.encrypted_content"}
		// Note: reasoningSummary is NOT sent by default (only for specific providers like "opencode")
	}

	return result
}

// MaxOutputTokens returns the appropriate max output tokens for a model.
// This matches opencode's defaults for reasoning vs non-reasoning models.
func MaxOutputTokens(modelID string) int {
	caps := GetModelCapabilities(modelID)
	if caps.DefaultMaxOutputTokens > 0 {
		return caps.DefaultMaxOutputTokens
	}
	return 16384 // Standard default
}
