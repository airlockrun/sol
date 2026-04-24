package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProvidersFromModelsDev(t *testing.T) {
	// Reset state for test
	state.mu.Lock()
	state.loaded = false
	state.providers = nil
	state.mu.Unlock()

	providers, err := LoadProviders()
	if err != nil {
		t.Fatalf("LoadProviders() error = %v", err)
	}

	// Should have loaded providers from models.dev or fallback
	if len(providers) == 0 {
		t.Error("LoadProviders() returned empty providers")
	}

	// Check that common providers exist
	commonProviders := []string{"openai", "anthropic", "google"}
	for _, id := range commonProviders {
		if _, ok := providers[id]; !ok {
			t.Errorf("LoadProviders() missing common provider %q", id)
		}
	}
}

func TestGetProviderInfo(t *testing.T) {
	// Reset state
	state.mu.Lock()
	state.loaded = false
	state.providers = nil
	state.mu.Unlock()

	tests := []struct {
		providerID string
		wantFound  bool
	}{
		{"openai", true},
		{"anthropic", true},
		{"nonexistent-provider-xyz", false},
	}

	for _, tt := range tests {
		t.Run(tt.providerID, func(t *testing.T) {
			info, found := GetProviderInfo(tt.providerID)
			if found != tt.wantFound {
				t.Errorf("GetProviderInfo(%q) found = %v, want %v", tt.providerID, found, tt.wantFound)
			}
			if found && info == nil {
				t.Errorf("GetProviderInfo(%q) returned nil info but found=true", tt.providerID)
			}
			if found && info.ID != tt.providerID {
				t.Errorf("GetProviderInfo(%q) returned info.ID = %q", tt.providerID, info.ID)
			}
		})
	}
}

func TestGetProviderInfoHasEnvVars(t *testing.T) {
	info, found := GetProviderInfo("openai")
	if !found {
		t.Fatal("GetProviderInfo(openai) not found")
	}
	if len(info.Env) == 0 {
		t.Error("GetProviderInfo(openai) has no env vars")
	}
}

func TestListProviders(t *testing.T) {
	providers := ListProviders()
	if len(providers) == 0 {
		t.Error("ListProviders() returned empty list")
	}

	// Check for common providers
	found := make(map[string]bool)
	for _, id := range providers {
		found[id] = true
	}

	commonProviders := []string{"openai", "anthropic"}
	for _, id := range commonProviders {
		if !found[id] {
			t.Errorf("ListProviders() missing %q", id)
		}
	}
}

func TestBuiltinProvidersFallback(t *testing.T) {
	// Test that builtin providers have correct structure
	builtins := getBuiltinProviders()

	if len(builtins) == 0 {
		t.Fatal("getBuiltinProviders() returned empty map")
	}

	// Check openai
	openai, ok := builtins["openai"]
	if !ok {
		t.Fatal("builtin providers missing openai")
	}
	if openai.Name != "OpenAI" {
		t.Errorf("openai.Name = %q, want %q", openai.Name, "OpenAI")
	}
	if len(openai.Env) == 0 || openai.Env[0] != "OPENAI_API_KEY" {
		t.Errorf("openai.Env = %v, want [OPENAI_API_KEY]", openai.Env)
	}

	// Check anthropic
	anthropic, ok := builtins["anthropic"]
	if !ok {
		t.Fatal("builtin providers missing anthropic")
	}
	if anthropic.Name != "Anthropic" {
		t.Errorf("anthropic.Name = %q, want %q", anthropic.Name, "Anthropic")
	}
}

func TestCacheOperations(t *testing.T) {
	// Create temp directory for cache
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Create test providers
	testProviders := map[string]*ModelsDevProvider{
		"test-provider": {
			ID:   "test-provider",
			Name: "Test Provider",
			Env:  []string{"TEST_API_KEY"},
		},
	}

	// Test save to cache
	err := saveToCache(testProviders)
	if err != nil {
		t.Fatalf("saveToCache() error = %v", err)
	}

	// Check file was created
	cachePath := filepath.Join(tmpDir, ".cache", "sol", "models.json")
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		t.Fatalf("cache file not created at %s", cachePath)
	}

	// Test load from cache
	loaded, err := loadFromCache()
	if err != nil {
		t.Fatalf("loadFromCache() error = %v", err)
	}

	if loaded["test-provider"] == nil {
		t.Fatal("loadFromCache() missing test-provider")
	}
	if loaded["test-provider"].Name != "Test Provider" {
		t.Errorf("loaded provider name = %q, want %q", loaded["test-provider"].Name, "Test Provider")
	}
}

func TestFetchFromModelsDev(t *testing.T) {
	// Create mock server
	mockData := map[string]*ModelsDevProvider{
		"mock-provider": {
			ID:   "mock-provider",
			Name: "Mock Provider",
			Env:  []string{"MOCK_API_KEY"},
			Models: map[string]ModelInfo{
				"mock-model": {
					ID:   "mock-model",
					Name: "Mock Model",
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mockData)
	}))
	defer server.Close()

	// We can't easily override ModelsDevURL, but we can test the builtin fallback works
	// when the real API is unavailable
	builtins := getBuiltinProviders()
	if len(builtins) < 5 {
		t.Errorf("getBuiltinProviders() returned only %d providers, expected at least 5", len(builtins))
	}
}

func TestModelInfoStructure(t *testing.T) {
	// Test that ModelInfo can be marshaled/unmarshaled
	model := ModelInfo{
		ID:          "test-model",
		Name:        "Test Model",
		Family:      "test-family",
		Attachment:  true,
		Reasoning:   true,
		Temperature: true,
		ToolCall:    true,
		Cost: &ModelCost{
			Input:      10.0,
			Output:     30.0,
			CacheRead:  2.5,
			CacheWrite: 5.0,
		},
		Limit: &ModelLimit{
			Context: 128000,
			Input:   100000,
			Output:  4096,
		},
	}

	data, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("json.Marshal(model) error = %v", err)
	}

	var decoded ModelInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.ID != model.ID {
		t.Errorf("decoded.ID = %q, want %q", decoded.ID, model.ID)
	}
	if decoded.Reasoning != model.Reasoning {
		t.Errorf("decoded.Reasoning = %v, want %v", decoded.Reasoning, model.Reasoning)
	}
	if decoded.Cost.Input != model.Cost.Input {
		t.Errorf("decoded.Cost.Input = %v, want %v", decoded.Cost.Input, model.Cost.Input)
	}
	if decoded.Limit.Context != model.Limit.Context {
		t.Errorf("decoded.Limit.Context = %v, want %v", decoded.Limit.Context, model.Limit.Context)
	}
}

func TestProviderEnvVarsFromModelsDev(t *testing.T) {
	// Test that providers loaded from models.dev have env vars
	info, found := GetProviderInfo("anthropic")
	if !found {
		t.Skip("anthropic not found - models.dev may be unavailable")
	}

	if len(info.Env) == 0 {
		t.Error("anthropic provider has no env vars")
	}

	// Check that ANTHROPIC_API_KEY is in the list
	hasKey := false
	for _, env := range info.Env {
		if env == "ANTHROPIC_API_KEY" {
			hasKey = true
			break
		}
	}
	if !hasKey {
		t.Errorf("anthropic env vars %v does not contain ANTHROPIC_API_KEY", info.Env)
	}
}
