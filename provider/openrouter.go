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

// OpenRouterAllModelsURL returns OpenRouter's ENTIRE catalog in one request —
// chat, vision, embeddings, image, speech (TTS), and transcription (STT) — via
// the output_modalities=all filter. The default /models list is chat-only and
// models.dev (our base catalog) mirrors just that, so this single request is the
// only source that also surfaces the non-chat modalities. No API key required.
const OpenRouterAllModelsURL = "https://openrouter.ai/api/v1/models?output_modalities=all"

type openRouterState struct {
	mu        sync.Mutex
	models    []ModelInfo
	loaded    bool
	fetching  bool
	lastFetch time.Time
}

var orState = &openRouterState{}

// openRouterModels returns OpenRouter's non-chat modality models (embedding,
// image, speech, transcription) as ModelInfo, classified by output modality and
// cached for RefreshInterval. It NEVER blocks: a cold or stale cache kicks off a
// single background refresh and returns the last-known result (nil on a cold
// start), so a transient OpenRouter outage degrades to "no extra OpenRouter
// models" rather than stalling the catalog. Chat/vision are left to models.dev;
// this merges additively on top (mergeOpenRouterModels), so an id collision
// keeps the models.dev entry. Warm it from StartPeriodicRefresh so the first
// catalog request usually already has the data.
func openRouterModels() []ModelInfo {
	orState.mu.Lock()
	defer orState.mu.Unlock()
	if orState.loaded && time.Since(orState.lastFetch) < RefreshInterval {
		return orState.models
	}
	if !orState.fetching {
		orState.fetching = true
		go refreshOpenRouterModels()
	}
	return orState.models
}

func refreshOpenRouterModels() {
	models, err := fetchOpenRouterModels()
	orState.mu.Lock()
	defer orState.mu.Unlock()
	orState.fetching = false
	if err != nil {
		log.Printf("sol/provider: openrouter models fetch failed: %v", err)
		return // keep last-known (possibly nil)
	}
	orState.models = models
	orState.loaded = true
	orState.lastFetch = time.Now()
}

// openRouterModelsResponse is the subset of OpenRouter's /models payload we
// consume. Pricing.Prompt is USD per token, serialized as a string.
type openRouterModelsResponse struct {
	Data []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContextLength int    `json:"context_length"`
		Pricing       struct {
			Prompt string `json:"prompt"`
		} `json:"pricing"`
		Architecture struct {
			OutputModalities []string `json:"output_modalities"`
		} `json:"architecture"`
	} `json:"data"`
}

func fetchOpenRouterModels() ([]ModelInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(OpenRouterAllModelsURL)
	if err != nil {
		return nil, fmt.Errorf("fetch openrouter models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter models returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openrouter models: %w", err)
	}
	return parseOpenRouterModels(body)
}

// openRouterKind maps a model's output modalities to a sol kind. Returns "" for
// chat/vision (output is text — left to models.dev) and for modalities sol has
// no slot for (video, rerank, audio-via-chat). "speech"/"transcription"/
// "embeddings"/"image" are OpenRouter's dedicated-modality output markers; a
// chat model that merely accepts/produces audio uses the "text"/"audio" tokens
// and is correctly not classified here.
func openRouterKind(out []string) ModelKind {
	has := func(s string) bool {
		for _, o := range out {
			if o == s {
				return true
			}
		}
		return false
	}
	switch {
	case has("speech"):
		return KindSpeech
	case has("transcription"):
		return KindTranscription
	case has("embeddings"):
		return KindEmbedding
	case has("image"):
		return KindImage
	}
	return ""
}

func parseOpenRouterModels(body []byte) ([]ModelInfo, error) {
	var parsed openRouterModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse openrouter models: %w", err)
	}
	out := make([]ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		kind := openRouterKind(m.Architecture.OutputModalities)
		if m.ID == "" || kind == "" {
			continue
		}
		mi := ModelInfo{ID: m.ID, Name: m.Name, Kind: kind, Modalities: modalitiesForKind(kind)}
		// pricing.prompt is per text token — valid only where billing is per
		// token: embeddings and speech (text→audio). Transcription bills per
		// audio unit and image per image, so pricing.prompt isn't per-token
		// there; leave Cost unset rather than record a nonsensical figure.
		if kind == KindEmbedding || kind == KindSpeech {
			if perTok, err := strconv.ParseFloat(m.Pricing.Prompt, 64); err == nil && perTok > 0 {
				mi.Cost = &ModelCost{Input: perTok * 1e6}
			}
		}
		if m.ContextLength > 0 {
			mi.Limit = &ModelLimit{Context: m.ContextLength}
		}
		out = append(out, mi)
	}
	return out, nil
}

// mergeOpenRouterModels additively folds OpenRouter-API-sourced models (embedding,
// speech, transcription, image) into the openrouter provider entry of an
// AllProviders result. Additive: existing entries (chat/vision from models.dev)
// are never overwritten. Clones the provider before mutating so LoadProviders'
// cache isn't poisoned. A nil/empty model list is a no-op.
func mergeOpenRouterModels(out map[string]*ModelsDevProvider, models []ModelInfo) {
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
