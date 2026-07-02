package provider

// AllProviders returns the full provider catalog: models.dev data enriched with
// sol-derived model kinds, the search-only provider stubs, and OpenRouter's
// non-chat modality catalog. Callers that want the live models.dev map
// unmodified should call LoadProviders directly.
//
// The catalog is fully dynamic: models.dev + OpenRouter, fetched and cached.
// The only hand-maintained entries are the search providers (overlay.go) —
// models.dev has no notion of web search. Nothing enumerates model IDs
// statically; a model absent from both dynamic sources isn't offered.
//
// Deprecated models (Status == "deprecated" per models.dev) are dropped so they
// don't surface in pickers / catalogs. Runtime lookups via GetModelInfo go
// through LoadProviders directly and so still resolve — agents configured on a
// now-deprecated model keep running, they're just hidden from the dropdown.
//
// Merge semantics, in order:
//
//  1. Start from models.dev's LoadProviders() result.
//  2. Synthesize stubs for search-only providers absent from models.dev
//     (e.g. brave). Their search capability is derived from SearchBackend.
//  3. Drop deprecated entries from every provider in the merged set.
//  4. Classify each model's Kind from its own properties (models.dev carries
//     no explicit kind): embeddings by name, image models by output modality.
//     Everything else is a language model; speech/transcription come from
//     OpenRouter in step 5.
//  5. Merge OpenRouter's non-chat modality catalog (openrouter.go) — embedding,
//     image, speech, transcription — which models.dev doesn't carry.
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

	// Step 4: classify each model's Kind from its own properties. models.dev
	// carries no explicit kind, so we derive it — embeddings by name, image
	// models by output modality (see derivedKind). Speech/transcription come
	// from OpenRouter (step 5); everything else stays a language model. Clone
	// only providers we actually restamp so LoadProviders' cache stays intact.
	for id, p := range out {
		needs := false
		for _, m := range p.Models {
			if m.Kind == "" && derivedKind(m) != "" {
				needs = true
				break
			}
		}
		if !needs {
			continue
		}
		clone := cloneProvider(p)
		for mid, m := range clone.Models {
			if m.Kind == "" {
				if k := derivedKind(m); k != "" {
					m.Kind = k
					clone.Models[mid] = m
				}
			}
		}
		out[id] = clone
	}

	// Step 5: OpenRouter's non-chat modalities (embedding, image, speech,
	// transcription), which models.dev doesn't carry. Fetched in ONE request
	// (output_modalities=all, cached + non-blocking), classified by output
	// modality, and merged additively into the openrouter provider — collisions
	// keep the models.dev chat entry.
	mergeOpenRouterModels(out, openRouterModels())

	return out, nil
}

// derivedKind classifies a models.dev entry by its own properties, since
// models.dev exposes no explicit kind. Embedding is name-based — models.dev
// reports embeddings as text→text, indistinguishable from chat otherwise, and
// no non-embedding model is named "embed". Image is output-modality based: a
// model whose output is image (and not text) is an image generator. Returns ""
// for a plain language model. Speech/transcription aren't derived here —
// models.dev doesn't reliably carry audio models; those arrive via OpenRouter.
func derivedKind(m ModelInfo) ModelKind {
	if isEmbeddingModel(m.ID, m.Name) {
		return KindEmbedding
	}
	if m.Modalities != nil && outputsImageOnly(m.Modalities.Output) {
		return KindImage
	}
	return ""
}

// outputsImageOnly reports whether a model's output modalities mark it as an
// image generator: image is present and text is not (a text-output model that
// can also emit images is a chat model, not an image model).
func outputsImageOnly(output []string) bool {
	hasImage := false
	for _, o := range output {
		switch o {
		case "text":
			return false
		case "image":
			hasImage = true
		}
	}
	return hasImage
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
