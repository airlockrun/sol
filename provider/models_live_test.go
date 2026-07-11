package provider

import (
	"context"
	"testing"
)

func TestLiveCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live catalog fetch in short mode")
	}
	providers, _, _, err := fetchCatalog(context.Background(), catalogHTTPClient, CatalogURL, "")
	if err != nil {
		t.Skipf("live catalog unavailable: %v", err)
	}
	if len(providers["openrouter"].Models) < 400 {
		t.Errorf("live OpenRouter catalog has %d models", len(providers["openrouter"].Models))
	}
}
