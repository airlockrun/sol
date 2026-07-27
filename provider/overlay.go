package provider

// Catalog enrichment owned by Sol. Model catalogs describe models and their
// modalities, but have no notion of which providers serve web search or which
// runtime-configured providers have no static model list. Those facts live here
// as small hand-maintained tables.
//
// Model kind enrichment lives in capabilities.go, and per-model attachment
// quirks live in attachment_policy.go.

// searchBackends maps a provider_id to the sol/websearch client name that
// serves web search for it. Presence in this map IS the "search" capability:
// a provider offers search iff it has an entry here. The backend name often
// differs from the provider_id because the websearch client kept its own
// historical name (xai→grok, google→gemini, moonshotai→kimi); for pure search
// providers it matches the id.
var searchBackends = map[string]string{
	"openai":     "openai", // web_search tool on the Responses API
	"xai":        "grok",   // reuses the LLM provider's API key
	"google":     "gemini",
	"moonshotai": "kimi",
	"perplexity": "perplexity",
	"brave":      "brave",
}

type reservedProvider struct {
	name         string
	capabilities CapabilitySet
}

// reservedProviders are Sol-owned catalog entries. Their IDs take precedence
// over source-catalog IDs and aliases, and never expose static models.
var reservedProviders = map[string]reservedProvider{
	"brave": {
		name: "Brave Search",
	},
	"openai-compatible": {
		name:         "OpenAI Compatible",
		capabilities: CapabilitySet{Text: true},
	},
}

// SearchBackend returns the sol/websearch backend client name for a provider,
// or "" if the provider doesn't offer web search.
func SearchBackend(providerID string) string {
	return searchBackends[providerID]
}
