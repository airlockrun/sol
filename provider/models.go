package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// ModelsDevURL is the URL for the models.dev API
	ModelsDevURL = "https://models.dev/api.json"

	// RefreshInterval is how often to refresh the models cache
	RefreshInterval = 60 * time.Minute
)

// ModelsDevProvider represents a provider from models.dev
type ModelsDevProvider struct {
	ID     string               `json:"id"`
	Name   string               `json:"name"`
	API    string               `json:"api,omitempty"`
	NPM    string               `json:"npm,omitempty"`
	Env    []string             `json:"env"`
	Models map[string]ModelInfo `json:"models"`
}

// ModelInfo represents a model from models.dev
type ModelInfo struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Family       string           `json:"family,omitempty"`
	ReleaseDate  string           `json:"release_date,omitempty"`
	Attachment   bool             `json:"attachment,omitempty"`
	Reasoning    bool             `json:"reasoning,omitempty"`
	Temperature  bool             `json:"temperature,omitempty"`
	ToolCall     bool             `json:"tool_call,omitempty"`
	Modalities   *ModelModalities `json:"modalities,omitempty"`
	Cost         *ModelCost       `json:"cost,omitempty"`
	Limit        *ModelLimit      `json:"limit,omitempty"`
	Status       string           `json:"status,omitempty"` // alpha, beta, deprecated
	Experimental bool             `json:"experimental,omitempty"`
}

// ModelModalities describes what input/output types a model supports.
type ModelModalities struct {
	Input  []string `json:"input"`  // e.g. ["text", "image", "pdf", "audio", "video"]
	Output []string `json:"output"` // e.g. ["text"]
}

// SupportsInput returns true if the model accepts the given input modality.
func (m *ModelModalities) SupportsInput(modality string) bool {
	if m == nil {
		return false
	}
	for _, v := range m.Input {
		if v == modality {
			return true
		}
	}
	return false
}

// ModelCost represents the cost structure for a model
type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

// ModelLimit represents token limits for a model
type ModelLimit struct {
	Context int `json:"context"`
	Input   int `json:"input,omitempty"`
	Output  int `json:"output"`
}

// modelsState holds the cached models data
type modelsState struct {
	mu        sync.RWMutex
	providers map[string]*ModelsDevProvider
	loaded    bool
	lastFetch time.Time
}

var state = &modelsState{}

// getCacheDir returns the cache directory for sol
func getCacheDir() string {
	// Try XDG_CACHE_HOME first
	if cacheDir := os.Getenv("XDG_CACHE_HOME"); cacheDir != "" {
		return filepath.Join(cacheDir, "sol")
	}
	// Fall back to ~/.cache/sol
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "sol")
	}
	return ""
}

// getCachePath returns the path to the models cache file
func getCachePath() string {
	cacheDir := getCacheDir()
	if cacheDir == "" {
		return ""
	}
	return filepath.Join(cacheDir, "models.json")
}

// loadFromCache tries to load providers from the cache file
func loadFromCache() (map[string]*ModelsDevProvider, error) {
	cachePath := getCachePath()
	if cachePath == "" {
		return nil, fmt.Errorf("no cache path")
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}

	var providers map[string]*ModelsDevProvider
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, err
	}

	return providers, nil
}

// saveToCache saves providers to the cache file
func saveToCache(providers map[string]*ModelsDevProvider) error {
	cachePath := getCachePath()
	if cachePath == "" {
		return fmt.Errorf("no cache path")
	}

	// Ensure cache directory exists
	cacheDir := filepath.Dir(cachePath)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}

	data, err := json.Marshal(providers)
	if err != nil {
		return err
	}

	return os.WriteFile(cachePath, data, 0644)
}

// fetchFromModelsDev fetches provider data from models.dev API
func fetchFromModelsDev() (map[string]*ModelsDevProvider, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(ModelsDevURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch models.dev: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var providers map[string]*ModelsDevProvider
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return providers, nil
}

// LoadProviders loads providers from cache or fetches from models.dev
// This is called lazily on first access
func LoadProviders() (map[string]*ModelsDevProvider, error) {
	state.mu.Lock()
	defer state.mu.Unlock()

	// Return cached data if available and not stale
	if state.loaded && time.Since(state.lastFetch) < RefreshInterval {
		return state.providers, nil
	}

	// Try loading from cache first
	if providers, err := loadFromCache(); err == nil && len(providers) > 0 {
		state.providers = providers
		state.loaded = true
		state.lastFetch = time.Now()

		// Trigger background refresh if cache is older than refresh interval
		go func() {
			if providers, err := fetchFromModelsDev(); err == nil {
				state.mu.Lock()
				state.providers = providers
				state.lastFetch = time.Now()
				state.mu.Unlock()
				saveToCache(providers)
			}
		}()

		return state.providers, nil
	}

	// Fetch from models.dev
	providers, err := fetchFromModelsDev()
	if err != nil {
		// If fetch fails and we have no cache, use built-in fallback
		if !state.loaded {
			state.providers = getBuiltinProviders()
			state.loaded = true
		}
		return state.providers, nil
	}

	state.providers = providers
	state.loaded = true
	state.lastFetch = time.Now()

	// Save to cache
	go saveToCache(providers)

	return state.providers, nil
}

// GetProviderInfo returns provider info from the models.dev data
func GetProviderInfo(providerID string) (*ModelsDevProvider, bool) {
	providers, err := LoadProviders()
	if err != nil {
		return nil, false
	}
	p, ok := providers[providerID]
	return p, ok
}

// GetModelInfo returns model info for a specific provider and model
func GetModelInfo(providerID, modelID string) (*ModelInfo, bool) {
	provider, ok := GetProviderInfo(providerID)
	if !ok {
		return nil, false
	}
	model, ok := provider.Models[modelID]
	if !ok {
		return nil, false
	}
	return &model, true
}

// getBuiltinProviders returns a fallback set of providers when models.dev is unavailable
func getBuiltinProviders() map[string]*ModelsDevProvider {
	return map[string]*ModelsDevProvider{
		"openai": {
			ID:   "openai",
			Name: "OpenAI",
			Env:  []string{"OPENAI_API_KEY"},
		},
		"anthropic": {
			ID:   "anthropic",
			Name: "Anthropic",
			Env:  []string{"ANTHROPIC_API_KEY"},
		},
		"google": {
			ID:   "google",
			Name: "Google",
			Env:  []string{"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"},
		},
		"azure": {
			ID:   "azure",
			Name: "Azure OpenAI",
			Env:  []string{"AZURE_OPENAI_API_KEY"},
		},
		"groq": {
			ID:   "groq",
			Name: "Groq",
			Env:  []string{"GROQ_API_KEY"},
		},
		"mistral": {
			ID:   "mistral",
			Name: "Mistral",
			Env:  []string{"MISTRAL_API_KEY"},
		},
		"xai": {
			ID:   "xai",
			Name: "xAI",
			Env:  []string{"XAI_API_KEY"},
		},
		"deepseek": {
			ID:   "deepseek",
			Name: "DeepSeek",
			Env:  []string{"DEEPSEEK_API_KEY"},
		},
		"github-copilot": {
			ID:   "github-copilot",
			Name: "GitHub Copilot",
			Env:  []string{"GITHUB_TOKEN"},
		},
		"openrouter": {
			ID:   "openrouter",
			Name: "OpenRouter",
			Env:  []string{"OPENROUTER_API_KEY"},
		},
		"together": {
			ID:   "together",
			Name: "Together AI",
			Env:  []string{"TOGETHER_API_KEY"},
		},
		"perplexity": {
			ID:   "perplexity",
			Name: "Perplexity",
			Env:  []string{"PERPLEXITY_API_KEY"},
		},
		"cohere": {
			ID:   "cohere",
			Name: "Cohere",
			Env:  []string{"COHERE_API_KEY"},
		},
		"amazon-bedrock": {
			ID:   "amazon-bedrock",
			Name: "Amazon Bedrock",
			Env:  []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"},
		},
		"google-vertex": {
			ID:   "google-vertex",
			Name: "Google Vertex AI",
			Env:  []string{"GOOGLE_APPLICATION_CREDENTIALS"},
		},
	}
}

// ListProviders returns all available provider IDs
func ListProviders() []string {
	providers, _ := LoadProviders()
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	return ids
}

// GetContextLimit returns the context limit for a model.
// Returns 0 if the model is not found or has no limit defined.
func GetContextLimit(providerID, modelID string) int {
	model, ok := GetModelInfo(providerID, modelID)
	if !ok || model.Limit == nil {
		return 0
	}
	return model.Limit.Context
}

// GetModalities returns the model's input/output modalities.
// Returns nil if the model is not found or has no modalities defined.
func GetModalities(providerID, modelID string) *ModelModalities {
	model, ok := GetModelInfo(providerID, modelID)
	if !ok || model.Modalities == nil {
		return nil
	}
	return model.Modalities
}

// SupportsInputModality returns true if the model supports the given input type.
// Returns true if model info is unavailable (optimistic fallback).
func SupportsInputModality(providerID, modelID, modality string) bool {
	m := GetModalities(providerID, modelID)
	if m == nil {
		return true // optimistic: allow if we don't know
	}
	return m.SupportsInput(modality)
}

// GetOutputLimit returns the output limit for a model.
// Returns 0 if the model is not found or has no limit defined.
func GetOutputLimit(providerID, modelID string) int {
	model, ok := GetModelInfo(providerID, modelID)
	if !ok || model.Limit == nil {
		return 0
	}
	return model.Limit.Output
}
