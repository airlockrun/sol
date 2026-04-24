package tools

import (
	"os"

	"github.com/airlockrun/goai/tool"
	solprovider "github.com/airlockrun/sol/provider"
	"github.com/airlockrun/sol/websearch"
)

// WebSearch returns a web search tool if a provider can be resolved.
// It first checks if the given LLM provider has a native search backend
// declared in the provider overlay (reusing the same API key), then falls
// back to dedicated search env vars for brave/perplexity (CLI use).
// Returns (tool, false) if no search provider is available.
//
// Airlock's runtime resolves search differently — it queries the providers
// table for any enabled search-capable row. This helper exists for the
// standalone Sol CLI and the build-time sol runner where there's no DB.
func WebSearch(llmProvider, llmAPIKey string) (tool.Tool, bool) {
	// 1. Can the LLM provider also do search? Reuse the same key.
	if ov, ok := solprovider.Overlay[llmProvider]; ok && ov.SearchBackend != "" {
		return websearch.NewTool(websearch.NewClient(websearch.Options{
			Provider: ov.SearchBackend,
			APIKey:   llmAPIKey,
		})), true
	}

	// 2. Fall back to dedicated search env vars (CLI/dev use).
	for _, p := range []struct{ provider, env string }{
		{"brave", "BRAVE_API_KEY"},
		{"perplexity", "PERPLEXITY_API_KEY"},
	} {
		if key := os.Getenv(p.env); key != "" {
			return websearch.NewTool(websearch.NewClient(websearch.Options{
				Provider: p.provider,
				APIKey:   key,
			})), true
		}
	}

	// 3. No search provider available.
	return tool.Tool{}, false
}
