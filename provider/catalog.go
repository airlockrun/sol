package provider

// AllProviders returns the full provider catalog: models.dev data merged
// with the hand-maintained Overlay. Callers that want the live models.dev
// map unmodified should call LoadProviders directly.
//
// Merge semantics:
//   - Providers present in both sources: start from the models.dev entry,
//     append Overlay.ExtraModels into the Models map (overlay wins on key
//     collisions so we can override stale upstream entries).
//   - Providers present only in Overlay (e.g. brave): a fresh
//     ModelsDevProvider is synthesized using Overlay.DisplayName; Models
//     starts empty and is populated from Overlay.ExtraModels if any.
//
// The returned map is a shallow copy of the cached LoadProviders result —
// safe to mutate at the top level without corrupting the cache, but the
// inner *ModelsDevProvider pointers are shared with the cache.
// Since we may add entries to p.Models, we clone each touched provider.
func AllProviders() (map[string]*ModelsDevProvider, error) {
	base, err := LoadProviders()
	if err != nil {
		return nil, err
	}

	out := make(map[string]*ModelsDevProvider, len(base)+len(Overlay))
	for id, p := range base {
		out[id] = p
	}

	for id, ov := range Overlay {
		existing, ok := out[id]
		if !ok {
			// Provider isn't in models.dev at all — synthesize a stub.
			stub := &ModelsDevProvider{
				ID:     id,
				Name:   ov.DisplayName,
				Models: map[string]ModelInfo{},
			}
			for _, em := range ov.ExtraModels {
				stub.Models[em.ID] = em
			}
			out[id] = stub
			continue
		}
		if len(ov.ExtraModels) == 0 {
			continue
		}
		// Clone the provider so we don't mutate the shared cache.
		clone := &ModelsDevProvider{
			ID:     existing.ID,
			Name:   existing.Name,
			API:    existing.API,
			NPM:    existing.NPM,
			Env:    existing.Env,
			Models: make(map[string]ModelInfo, len(existing.Models)+len(ov.ExtraModels)),
		}
		for k, v := range existing.Models {
			clone.Models[k] = v
		}
		for _, em := range ov.ExtraModels {
			clone.Models[em.ID] = em
		}
		out[id] = clone
	}

	return out, nil
}
