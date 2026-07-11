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
}
