package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sync"
	"time"
)

const (
	CatalogURL             = "https://models.airlock.run/models.json"
	RefreshInterval        = 12 * time.Hour
	failedRefreshRetry     = 5 * time.Minute
	maxCatalogResponseSize = 16 << 20
	minimumProviderCount   = 100
	minimumModelCount      = 1000
)

// ModelsDevProvider represents one provider in the models.dev-compatible
// Airlock model catalog.
type ModelsDevProvider struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	API     string               `json:"api,omitempty"`
	NPM     string               `json:"npm,omitempty"`
	Env     []string             `json:"env"`
	Aliases []string             `json:"aliases,omitempty"`
	Models  map[string]ModelInfo `json:"models"`
}

// ModelInfo represents a model in the Airlock catalog.
type ModelInfo struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Kind         ModelKind        `json:"kind,omitempty"`
	Family       string           `json:"family,omitempty"`
	ReleaseDate  string           `json:"release_date,omitempty"`
	Attachment   bool             `json:"attachment,omitempty"`
	Reasoning    bool             `json:"reasoning,omitempty"`
	Temperature  bool             `json:"temperature,omitempty"`
	ToolCall     bool             `json:"tool_call,omitempty"`
	Modalities   *ModelModalities `json:"modalities,omitempty"`
	Cost         *ModelCost       `json:"cost,omitempty"`
	Limit        *ModelLimit      `json:"limit,omitempty"`
	Status       string           `json:"status,omitempty"`
	Experimental json.RawMessage  `json:"experimental,omitempty"`
}

type ModelModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

func (m *ModelModalities) SupportsInput(modality string) bool {
	if m == nil {
		return false
	}
	for _, value := range m.Input {
		if value == modality {
			return true
		}
	}
	return false
}

type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

type ModelLimit struct {
	Context int `json:"context"`
	Input   int `json:"input,omitempty"`
	Output  int `json:"output"`
}

type modelsState struct {
	mu               sync.RWMutex
	providers        map[string]*ModelsDevProvider
	etag             string
	refreshing       bool
	refresherStarted bool
	lastAttempt      time.Time
	lastSuccess      time.Time
}

var state = &modelsState{providers: mustDecodeEmbeddedCatalog()}

var catalogHTTPClient = &http.Client{Timeout: 10 * time.Second}

func mustDecodeEmbeddedCatalog() map[string]*ModelsDevProvider {
	providers, err := decodeCatalog(embeddedCatalogJSON, true)
	if err != nil {
		panic("sol/provider: invalid embedded catalog: " + err.Error())
	}
	return providers
}

func decodeCatalog(data []byte, enforceSizeFloor bool) (map[string]*ModelsDevProvider, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var providers map[string]*ModelsDevProvider
	if err := decoder.Decode(&providers); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := validateCatalog(providers, enforceSizeFloor); err != nil {
		return nil, err
	}
	return providers, nil
}

// ValidateCatalogJSON validates a candidate for the embedded catalog snapshot.
func ValidateCatalogJSON(data []byte) error {
	_, err := decodeCatalog(data, true)
	return err
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing catalog data: %w", err)
	}
	return errors.New("catalog contains multiple JSON values")
}

func validateCatalog(providers map[string]*ModelsDevProvider, enforceSizeFloor bool) error {
	if len(providers) == 0 {
		return errors.New("catalog is empty")
	}
	models := 0
	for providerID, provider := range providers {
		if providerID == "" || provider == nil || provider.ID != providerID || provider.Name == "" || provider.Models == nil {
			return fmt.Errorf("provider %q is invalid", providerID)
		}
		for modelID, model := range provider.Models {
			models++
			if modelID == "" || model.ID != modelID || model.Name == "" {
				return fmt.Errorf("model %q/%q is invalid", providerID, modelID)
			}
			if !validModelKind(model.Kind) {
				return fmt.Errorf("model %q/%q has invalid kind %q", providerID, modelID, model.Kind)
			}
			if model.Status != "" && model.Status != "alpha" && model.Status != "beta" && model.Status != "deprecated" {
				return fmt.Errorf("model %q/%q has invalid status %q", providerID, modelID, model.Status)
			}
			if model.Modalities != nil {
				for _, modality := range append(append([]string{}, model.Modalities.Input...), model.Modalities.Output...) {
					if !validModality(modality) {
						return fmt.Errorf("model %q/%q has invalid modality %q", providerID, modelID, modality)
					}
				}
			}
			if model.Cost != nil && (!validNumber(model.Cost.Input) || !validNumber(model.Cost.Output) || !validNumber(model.Cost.CacheRead) || !validNumber(model.Cost.CacheWrite)) {
				return fmt.Errorf("model %q/%q has invalid cost", providerID, modelID)
			}
			if model.Limit != nil && (model.Limit.Context < 0 || model.Limit.Input < 0 || model.Limit.Output < 0) {
				return fmt.Errorf("model %q/%q has invalid limit", providerID, modelID)
			}
		}
	}
	for _, required := range []string{"openai", "anthropic", "google"} {
		if providers[required] == nil {
			return fmt.Errorf("catalog is missing provider %q", required)
		}
	}
	if enforceSizeFloor && (len(providers) < minimumProviderCount || models < minimumModelCount) {
		return fmt.Errorf("catalog has %d providers / %d models; minimum is %d / %d", len(providers), models, minimumProviderCount, minimumModelCount)
	}
	return nil
}

func validModelKind(kind ModelKind) bool {
	switch kind {
	case KindLanguage, KindEmbedding, KindImage, KindAudio, KindVideo, KindSpeech, KindTranscription, KindReranking:
		return true
	default:
		return false
	}
}

func validModality(modality string) bool {
	switch modality {
	case "text", "image", "audio", "video", "pdf", "file", "embeddings", "speech", "transcription", "rerank":
		return true
	default:
		return false
	}
}

func validNumber(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func fetchCatalog(ctx context.Context, client *http.Client, url, etag string) (map[string]*ModelsDevProvider, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sol-model-catalog/1")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("catalog returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogResponseSize+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("read catalog: %w", err)
	}
	if len(body) > maxCatalogResponseSize {
		return nil, "", false, fmt.Errorf("catalog exceeds %d bytes", maxCatalogResponseSize)
	}
	providers, err := decodeCatalog(body, true)
	if err != nil {
		return nil, "", false, err
	}
	return providers, resp.Header.Get("ETag"), false, nil
}

// LoadProviders returns the active validated catalog immediately. The embedded
// snapshot is always available; the first call starts a non-blocking refresh.
func LoadProviders() (map[string]*ModelsDevProvider, error) {
	triggerRefresh(context.Background(), "lazy", false)
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.providers, nil
}

func triggerRefresh(ctx context.Context, tag string, force bool) {
	state.mu.Lock()
	if state.refreshing {
		state.mu.Unlock()
		return
	}
	if !force && !state.lastAttempt.IsZero() {
		retryAfter := failedRefreshRetry
		if !state.lastSuccess.IsZero() {
			retryAfter = RefreshInterval
		}
		if time.Since(state.lastAttempt) < retryAfter {
			state.mu.Unlock()
			return
		}
	}
	state.refreshing = true
	state.lastAttempt = time.Now()
	etag := state.etag
	state.mu.Unlock()

	go func() {
		providers, nextETag, notModified, err := fetchCatalog(ctx, catalogHTTPClient, CatalogURL, etag)
		state.mu.Lock()
		defer state.mu.Unlock()
		state.refreshing = false
		if err != nil {
			log.Printf("sol/provider: catalog %s refresh failed: %v", tag, err)
			return
		}
		if notModified {
			state.lastSuccess = time.Now()
			return
		}
		state.providers = providers
		state.etag = nextETag
		state.lastSuccess = time.Now()
	}()
}

// StartPeriodicRefresh starts one immediate refresh and one periodic ticker.
func StartPeriodicRefresh(ctx context.Context) {
	state.mu.Lock()
	if state.refresherStarted {
		state.mu.Unlock()
		return
	}
	state.refresherStarted = true
	state.mu.Unlock()

	triggerRefresh(ctx, "startup", true)
	go func() {
		ticker := time.NewTicker(RefreshInterval)
		defer ticker.Stop()
		defer func() {
			state.mu.Lock()
			state.refresherStarted = false
			state.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				triggerRefresh(ctx, "periodic", true)
			}
		}
	}()
}

func GetProviderInfo(providerID string) (*ModelsDevProvider, bool) {
	providers, _ := LoadProviders()
	provider, ok := providers[providerID]
	if ok {
		return provider, true
	}
	for _, candidate := range providers {
		for _, alias := range candidate.Aliases {
			if alias == providerID {
				return candidate, true
			}
		}
	}
	return nil, false
}

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

func ListProviders() []string {
	providers, _ := LoadProviders()
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	return ids
}

func GetContextLimit(providerID, modelID string) int {
	model, ok := GetModelInfo(providerID, modelID)
	if !ok || model.Limit == nil {
		return 0
	}
	return model.Limit.Context
}

func GetModalities(providerID, modelID string) *ModelModalities {
	model, ok := GetModelInfo(providerID, modelID)
	if !ok || model.Modalities == nil {
		return nil
	}
	return model.Modalities
}

func SupportsInputModality(providerID, modelID, modality string) bool {
	modalities := GetModalities(providerID, modelID)
	if modalities == nil {
		return true
	}
	return modalities.SupportsInput(modality)
}

func GetOutputLimit(providerID, modelID string) int {
	model, ok := GetModelInfo(providerID, modelID)
	if !ok || model.Limit == nil {
		return 0
	}
	return model.Limit.Output
}
