package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedCatalog(t *testing.T) {
	providers, err := decodeCatalog(embeddedCatalogJSON, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"openai", "anthropic", "google", "openrouter", "deepinfra"} {
		if providers[id] == nil {
			t.Errorf("embedded catalog missing provider %q", id)
		}
	}
	if got := len(providers["openrouter"].Models); got < 400 {
		t.Errorf("embedded OpenRouter models = %d, want at least 400", got)
	}
	kinds := map[ModelKind]int{}
	for _, provider := range providers {
		for _, model := range provider.Models {
			kinds[model.Kind]++
		}
	}
	for _, kind := range []ModelKind{KindLanguage, KindEmbedding, KindImage, KindAudio, KindVideo, KindSpeech, KindTranscription, KindReranking} {
		if kinds[kind] == 0 {
			t.Errorf("embedded OpenRouter catalog has no %s models", kind)
		}
	}
}

func TestLoadProvidersUsesEmbeddedCatalog(t *testing.T) {
	providers, err := LoadProviders()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) < minimumProviderCount {
		t.Fatalf("LoadProviders() returned %d providers", len(providers))
	}
}

func TestGetProviderInfoResolvesPublishedAlias(t *testing.T) {
	provider, ok := GetProviderInfo("fireworks")
	if !ok {
		t.Fatal("GetProviderInfo(fireworks) did not resolve published alias")
	}
	if provider.ID != "fireworks-ai" {
		t.Fatalf("provider ID = %q, want fireworks-ai", provider.ID)
	}
}

func TestDecodeCatalogValidation(t *testing.T) {
	valid := testCatalogJSON(t)
	tests := []struct {
		name string
		data string
	}{
		{name: "malformed", data: `{`},
		{name: "trailing value", data: string(valid) + `{}`},
		{name: "empty", data: `{}`},
		{name: "provider id mismatch", data: `{"openai":{"id":"other","name":"OpenAI","models":{}},"anthropic":{"id":"anthropic","name":"Anthropic","models":{}},"google":{"id":"google","name":"Google","models":{}}}`},
		{name: "missing model name", data: `{"openai":{"id":"openai","name":"OpenAI","models":{"gpt":{"id":"gpt"}}},"anthropic":{"id":"anthropic","name":"Anthropic","models":{}},"google":{"id":"google","name":"Google","models":{}}}`},
		{name: "invalid status", data: `{"openai":{"id":"openai","name":"OpenAI","models":{"gpt":{"id":"gpt","name":"GPT","status":"broken"}}},"anthropic":{"id":"anthropic","name":"Anthropic","models":{}},"google":{"id":"google","name":"Google","models":{}}}`},
		{name: "invalid modality", data: `{"openai":{"id":"openai","name":"OpenAI","models":{"gpt":{"id":"gpt","name":"GPT","modalities":{"input":["smell"]}}}},"anthropic":{"id":"anthropic","name":"Anthropic","models":{}},"google":{"id":"google","name":"Google","models":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeCatalog([]byte(tt.data), false); err == nil {
				t.Fatal("decodeCatalog() error = nil")
			}
		})
	}
}

func TestFetchCatalog(t *testing.T) {
	catalog := embeddedCatalogJSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"current"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"next"`)
		_, _ = w.Write(catalog)
	}))
	defer server.Close()

	providers, etag, notModified, err := fetchCatalog(context.Background(), server.Client(), server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) < minimumProviderCount || etag != `"next"` || notModified {
		t.Fatalf("fetchCatalog() = %d providers, %q, %v", len(providers), etag, notModified)
	}
	providers, etag, notModified, err = fetchCatalog(context.Background(), server.Client(), server.URL, `"current"`)
	if err != nil {
		t.Fatal(err)
	}
	if providers != nil || etag != `"current"` || !notModified {
		t.Fatalf("304 fetch = %#v, %q, %v", providers, etag, notModified)
	}
}

func TestFetchCatalogRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http error", status: http.StatusBadGateway, body: "bad gateway"},
		{name: "malformed", status: http.StatusOK, body: `{`},
		{name: "undersized", status: http.StatusOK, body: string(testCatalogJSON(t))},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat(" ", maxCatalogResponseSize+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			if _, _, _, err := fetchCatalog(context.Background(), server.Client(), server.URL, ""); err == nil {
				t.Fatal("fetchCatalog() error = nil")
			}
		})
	}
}

func TestModelInfoRoundTrip(t *testing.T) {
	want := ModelInfo{ID: "model", Name: "Model", Kind: KindSpeech, Cost: &ModelCost{Input: 1}, Limit: &ModelLimit{Context: 100}}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ModelInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Kind != want.Kind || got.Cost.Input != want.Cost.Input {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func testCatalogJSON(t *testing.T) []byte {
	t.Helper()
	providers := map[string]*ModelsDevProvider{}
	for _, id := range []string{"openai", "anthropic", "google"} {
		providers[id] = &ModelsDevProvider{ID: id, Name: id, Models: map[string]ModelInfo{}}
	}
	data, err := json.Marshal(providers)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
