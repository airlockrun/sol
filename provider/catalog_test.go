package provider

import "testing"

func TestAllProvidersMergesSearchOverlay(t *testing.T) {
	all, err := AllProviders()
	if err != nil {
		t.Fatal(err)
	}
	brave, ok := all["brave"]
	if !ok {
		t.Fatal("AllProviders() missing Brave search provider")
	}
	if brave.Name != "Brave Search" || len(brave.Models) != 0 {
		t.Errorf("Brave provider = %+v", brave)
	}
	openAI, ok := all["openai"]
	if !ok {
		t.Fatal("AllProviders() missing OpenAI")
	}
	if !ProviderCapabilities(openAI).Search {
		t.Error("OpenAI should have search capability")
	}
	compat, ok := all["openai-compatible"]
	if !ok {
		t.Fatal("AllProviders() missing OpenAI Compatible provider")
	}
	if compat.Name != "OpenAI Compatible" || len(compat.Models) != 0 {
		t.Errorf("OpenAI Compatible provider = %+v", compat)
	}
	if got := ProviderCapabilities(compat); got != (CapabilitySet{Text: true}) {
		t.Errorf("OpenAI Compatible capabilities = %+v, want text only", got)
	}
}

func TestCatalogAPIsIncludeOpenAICompatible(t *testing.T) {
	info, ok := GetProviderInfo("openai-compatible")
	if !ok {
		t.Fatal("GetProviderInfo() missing openai-compatible")
	}
	if info.Name != "OpenAI Compatible" || len(info.Models) != 0 {
		t.Errorf("GetProviderInfo() = %+v", info)
	}

	for _, id := range ListProviders() {
		if id == "openai-compatible" {
			return
		}
	}
	t.Error("ListProviders() missing openai-compatible")
}

func TestAllProvidersReservedEntryOverridesCatalog(t *testing.T) {
	base := map[string]*ModelsDevProvider{
		"openai-compatible": {
			ID:   "openai-compatible",
			Name: "Remote Provider",
			API:  "https://remote.invalid/v1",
			Models: map[string]ModelInfo{
				"remote-model": {ID: "remote-model", Name: "Remote Model", Kind: KindLanguage},
			},
		},
	}

	all := mergeProviderCatalog(base)
	compat := all["openai-compatible"]
	if compat.Name != "OpenAI Compatible" || compat.API != "" || len(compat.Models) != 0 {
		t.Errorf("reserved provider = %+v", compat)
	}
}

func TestReservedEntryOverridesCatalogAlias(t *testing.T) {
	base := map[string]*ModelsDevProvider{
		"remote-provider": {
			ID:      "remote-provider",
			Name:    "Remote Provider",
			Aliases: []string{"openai-compatible"},
			Models: map[string]ModelInfo{
				"remote-model": {ID: "remote-model", Name: "Remote Model", Kind: KindLanguage},
			},
		},
	}

	compat, ok := getProviderInfo(base, "openai-compatible")
	if !ok {
		t.Fatal("getProviderInfo() missing reserved provider")
	}
	if compat.ID != "openai-compatible" || compat.Name != "OpenAI Compatible" || len(compat.Models) != 0 {
		t.Errorf("reserved provider = %+v", compat)
	}
}
