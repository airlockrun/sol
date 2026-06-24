package provider

// AllProviders returns the full provider catalog: models.dev data enriched
// with hand-maintained extras (search-only provider stubs, goai's typed kind
// lists, name-based + OpenRouter embedding classification). Callers that want
// the live models.dev map unmodified should call LoadProviders directly.
//
// Deprecated models (Status == "deprecated" per models.dev) are dropped
// before the goai merge so they don't surface in pickers / catalogs.
// Runtime lookups via GetModelInfo go through LoadProviders directly and
// so still resolve — agents configured on a now-deprecated model keep
// running, they're just hidden from the agent-create dropdown going forward.
//
// Merge semantics, in order:
//
//  1. Start from models.dev's LoadProviders() result.
//  2. Synthesize stubs for search-only providers absent from models.dev
//     (e.g. brave). Their search capability is derived from SearchBackend.
//  3. Drop deprecated entries from every provider in the merged set.
//  4. Layer goai: for each provider goai has typed-list coverage for
//     (see goaiProviderFactories), stamp ModelInfo.Kind on existing
//     entries and synthesize new ModelInfo for goai-listed IDs that
//     models.dev doesn't ship (e.g. openai's whisper-1, tts-1). Goai is
//     authoritative for Kind; modalities, cost, and limits come from
//     whichever source has them (models.dev wins, kind-derived defaults
//     fall in for the rest).
//  5. Name-based embedding classification (any model id/name with "embed").
//  6. Merge OpenRouter's dedicated embeddings catalog (openrouter.go).
//
// The returned map's inner *ModelsDevProvider pointers are clones whenever
// we touched the provider, so callers can mutate top-level safely. Caches
// are unchanged.
func AllProviders() (map[string]*ModelsDevProvider, error) {
	base, err := LoadProviders()
	if err != nil {
		return nil, err
	}

	out := make(map[string]*ModelsDevProvider, len(base)+len(searchOnlyProviders))
	for id, p := range base {
		out[id] = p
	}

	// Step 2: synthesize stubs for search-only providers that models.dev
	// doesn't list at all (e.g. Brave), so they still surface as configurable
	// providers. Their "search" capability is derived later from SearchBackend.
	for id, displayName := range searchOnlyProviders {
		if _, ok := out[id]; ok {
			continue
		}
		out[id] = &ModelsDevProvider{
			ID:     id,
			Name:   displayName,
			Models: map[string]ModelInfo{},
		}
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

	// Step 4: goai kind merge. Walk every provider goai has typed-list
	// coverage for, even ones absent from models.dev (e.g. fal, luma —
	// they ship via goai but aren't in models.dev's catalog).
	for providerID := range goaiProviderFactories {
		kinds := goaiKindLists(providerID)
		if len(kinds) == 0 {
			continue
		}

		existing, ok := out[providerID]
		if !ok {
			// Synthesize a stub provider for goai-only entries.
			existing = &ModelsDevProvider{
				ID:     providerID,
				Name:   providerID,
				Models: map[string]ModelInfo{},
			}
			out[providerID] = existing
		} else {
			// Clone before mutating so we don't poison the LoadProviders cache.
			existing = cloneProvider(existing)
			out[providerID] = existing
		}

		for kind, ids := range kinds {
			for _, id := range ids {
				m, present := existing.Models[id]
				if present {
					// models.dev/Overlay already has this model — only
					// stamp Kind if not already set.
					if m.Kind == "" {
						m.Kind = kind
						existing.Models[id] = m
					}
					continue
				}
				// goai-only model — synthesize a usable entry.
				existing.Models[id] = ModelInfo{
					ID:         id,
					Name:       id,
					Kind:       kind,
					Modalities: modalitiesForKind(kind),
				}
			}
		}
	}

	// Step 5: name-based embedding classification. models.dev exposes no
	// embedding modality or type, so an embedding model is indistinguishable
	// from a text→text model by its catalog entry. Rather than hand-curate
	// per-provider lists, treat any model whose id/name contains "embed" as an
	// embedding model — giving embedding coverage across every provider. Name
	// is authoritative (no non-embedding model is named "embed"); a rare false
	// positive just errors when called. Clone only providers we touch so
	// LoadProviders' cache stays intact.
	for id, p := range out {
		needs := false
		for _, m := range p.Models {
			if m.Kind != KindEmbedding && isEmbeddingModel(m.ID, m.Name) {
				needs = true
				break
			}
		}
		if !needs {
			continue
		}
		clone := cloneProvider(p)
		for mid, m := range clone.Models {
			if isEmbeddingModel(m.ID, m.Name) {
				m.Kind = KindEmbedding
				clone.Models[mid] = m
			}
		}
		out[id] = clone
	}

	// Step 6: OpenRouter's non-chat modalities (embedding, image, speech,
	// transcription), which models.dev doesn't carry. Fetched in ONE request
	// (output_modalities=all, cached + non-blocking), classified by output
	// modality, and merged additively into the openrouter provider — collisions
	// keep the models.dev chat entry.
	mergeOpenRouterModels(out, openRouterModels())

	return out, nil
}

// cloneProvider returns a shallow copy of p with a fresh Models map so
// mutations don't leak into the LoadProviders cache.
func cloneProvider(p *ModelsDevProvider) *ModelsDevProvider {
	clone := &ModelsDevProvider{
		ID:     p.ID,
		Name:   p.Name,
		API:    p.API,
		NPM:    p.NPM,
		Env:    p.Env,
		Models: make(map[string]ModelInfo, len(p.Models)),
	}
	for k, v := range p.Models {
		clone.Models[k] = v
	}
	return clone
}
