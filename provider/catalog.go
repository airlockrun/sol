package provider

// AllProviders returns the Airlock model catalog enriched with reserved
// provider stubs. Callers that want the source catalog unmodified should call
// LoadProviders directly.
//
// The catalog is remotely refreshed and has an embedded fallback. The only
// hand-maintained entries in Sol are reserved providers (overlay.go).
//
// Deprecated models (Status == "deprecated") are dropped so they don't surface
// in pickers / catalogs. Runtime lookups for source-catalog providers use
// LoadProviders and still resolve, so configured deprecated models keep running
// while remaining hidden from the dropdown.
//
// Merge semantics, in order:
//
//  1. Start from LoadProviders().
//  2. Overlay reserved providers with model-free stubs.
//  3. Drop deprecated entries from every provider in the merged set.
//
// The returned map's inner *ModelsDevProvider pointers are clones whenever
// we touched the provider, so callers can mutate top-level safely. Caches
// are unchanged.
func AllProviders() (map[string]*ModelsDevProvider, error) {
	base, err := LoadProviders()
	if err != nil {
		return nil, err
	}
	return mergeProviderCatalog(base), nil
}

func mergeProviderCatalog(base map[string]*ModelsDevProvider) map[string]*ModelsDevProvider {
	out := make(map[string]*ModelsDevProvider, len(base)+len(reservedProviders))
	for id, p := range base {
		out[id] = p
	}

	// Step 2: overwrite source entries whose IDs are reserved by Sol. This keeps
	// remotely supplied metadata and models out of runtime-configured providers.
	for id, entry := range reservedProviders {
		out[id] = reservedProviderInfo(id, entry)
	}

	// Step 3: drop deprecated models. We iterate every provider in the
	// merged set; only providers that actually contain deprecated entries
	// are cloned (so we don't poison LoadProviders' cache for the rest).
	for id, p := range out {
		hasDeprecated := false
		for _, m := range p.Models {
			if m.Status == "deprecated" {
				hasDeprecated = true
				break
			}
		}
		if !hasDeprecated {
			continue
		}
		clone := cloneProvider(p)
		for mid, m := range clone.Models {
			if m.Status == "deprecated" {
				delete(clone.Models, mid)
			}
		}
		out[id] = clone
	}

	return out
}

func reservedProviderInfo(id string, entry reservedProvider) *ModelsDevProvider {
	return &ModelsDevProvider{
		ID:     id,
		Name:   entry.name,
		Models: map[string]ModelInfo{},
	}
}

// cloneProvider returns a shallow copy of p with a fresh Models map so
// mutations don't leak into the LoadProviders cache.
func cloneProvider(p *ModelsDevProvider) *ModelsDevProvider {
	clone := &ModelsDevProvider{
		ID:      p.ID,
		Name:    p.Name,
		API:     p.API,
		NPM:     p.NPM,
		Env:     p.Env,
		Aliases: p.Aliases,
		Models:  make(map[string]ModelInfo, len(p.Models)),
	}
	for k, v := range p.Models {
		clone.Models[k] = v
	}
	return clone
}
