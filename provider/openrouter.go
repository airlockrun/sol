package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// OpenRouterEmbeddingModelsURL is OpenRouter's public embeddings catalog.
// Unlike chat models (which models.dev mirrors and OpenRouter also lists at
// /api/v1/models), OpenRouter keeps embedding models on this separate
// endpoint — so they appear in neither models.dev nor the main /models list.
// No API key is required to read it.
const OpenRouterEmbeddingModelsURL = "https://openrouter.ai/api/v1/embeddings/models"

type openRouterState struct {
	mu        sync.Mutex
	models    []ModelInfo
	loaded    bool
	fetching  bool
	lastFetch time.Time
}

var orState = &openRouterState{}

// openRouterEmbeddingModels returns OpenRouter's embedding models as ModelInfo
// (Kind=embedding), cached for RefreshInterval. It NEVER blocks: when the cache
// is cold or stale it kicks off a single background refresh and returns the
// last-known result (nil on a cold start). A transient OpenRouter outage simply
// degrades to "no OpenRouter embeddings" rather than stalling or breaking the
// catalog. Warm it eagerly from StartPeriodicRefresh so the first catalog
// request usually already has the data.
func openRouterEmbeddingModels() []ModelInfo {
	orState.mu.Lock()
	defer orState.mu.Unlock()
	if orState.loaded && time.Since(orState.lastFetch) < RefreshInterval {
		return orState.models
	}
	if !orState.fetching {
		orState.fetching = true
		go refreshOpenRouterEmbeddingModels()
	}
	return orState.models
}

func refreshOpenRouterEmbeddingModels() {
	models, err := fetchOpenRouterEmbeddingModels()
	orState.mu.Lock()
	defer orState.mu.Unlock()
	orState.fetching = false
	if err != nil {
		log.Printf("sol/provider: openrouter embeddings fetch failed: %v", err)
		return // keep last-known (possibly nil)
	}
	orState.models = models
	orState.loaded = true
	orState.lastFetch = time.Now()
}

// openRouterModelsResponse is the subset of OpenRouter's /embeddings/models
// payload we consume. Pricing.Prompt is USD per token, serialized as a string.
type openRouterModelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContextLength int    `json:"context_length"`
		Pricing       struct {
			Prompt string `json:"prompt"`
		} `json:"pricing"`
	} `json:"data"`
}

func fetchOpenRouterEmbeddingModels() ([]ModelInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(OpenRouterEmbeddingModelsURL)
	if err != nil {
		return nil, fmt.Errorf("fetch openrouter embeddings: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter embeddings returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openrouter embeddings: %w", err)
	}
	return parseOpenRouterEmbeddingModels(body)
}

func parseOpenRouterEmbeddingModels(body []byte) ([]ModelInfo, error) {
	var parsed openRouterModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse openrouter embeddings: %w", err)
	}
	out := make([]ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		mi := ModelInfo{
			ID:         m.ID,
			Name:       m.Name,
			Kind:       KindEmbedding,
			Modalities: &ModelModalities{Input: []string{"text"}, Output: []string{"text"}},
		}
		// OpenRouter prices per token; models.dev (our cost convention) is per
		// million tokens. Embeddings bill input only.
		if perTok, err := strconv.ParseFloat(m.Pricing.Prompt, 64); err == nil && perTok > 0 {
			mi.Cost = &ModelCost{Input: perTok * 1e6}
		}
		if m.ContextLength > 0 {
			mi.Limit = &ModelLimit{Context: m.ContextLength}
		}
		out = append(out, mi)
	}
	return out, nil
}

// mergeOpenRouterEmbeddings additively folds OpenRouter's embedding models into
// the openrouter provider entry of an AllProviders result. Additive: existing
// entries (the chat models from models.dev) are never overwritten. Clones the
// provider before mutating so LoadProviders' cache isn't poisoned. A nil/empty
// model list is a no-op.
func mergeOpenRouterEmbeddings(out map[string]*ModelsDevProvider, models []ModelInfo) {
	if len(models) == 0 {
		return
	}
	existing, ok := out["openrouter"]
	if !ok {
		existing = &ModelsDevProvider{ID: "openrouter", Name: "OpenRouter", Models: map[string]ModelInfo{}}
		out["openrouter"] = existing
	} else {
		existing = cloneProvider(existing)
		out["openrouter"] = existing
	}
	for _, m := range models {
		if _, present := existing.Models[m.ID]; !present {
			existing.Models[m.ID] = m
		}
	}
}
