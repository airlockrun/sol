package websearch

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/airlockrun/sol/provider"
)

// datedSnapshot matches a trailing models.dev date suffix, e.g.
// "gpt-5-nano-2025-08-07". We prefer the canonical floating alias over a pinned
// snapshot so the default tracks the provider's latest.
var datedSnapshot = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// searchExcludeTokens are id substrings we never auto-pick as a search default
// even when the model is language + tool_call: coding-tuned variants make poor
// general web-search synthesizers (e.g. kimi-k2.7-code).
var searchExcludeTokens = []string{"code"}

// pickSearchModel chooses a default web-search model for a models.dev provider
// id from the live catalog, so a deprecated or renamed model never requires a
// code change. It keeps non-deprecated language models that support tool calling
// (web search runs as a hosted tool), then requires a match against the
// provider's small/fast tier (tierTokens, in priority order — the latency-
// friendly models search wants), picking the best of that tier. Returns "" when
// no tier token matches, so the caller falls back to its pinned constant: we
// never silently substitute an off-tier model (a vanished tier is a signal, and
// the autopick test asserts a match for every backend so it surfaces loudly).
func pickSearchModel(providerID string, tierTokens []string) string {
	cat, err := provider.AllProviders()
	if err != nil {
		return ""
	}
	p := cat[providerID]
	if p == nil {
		return ""
	}
	var candidates []provider.ModelInfo
	for _, m := range p.Models {
		// AllProviders already drops deprecated models; re-check so the policy
		// is explicit here and survives a change in the catalog source.
		if m.Status == "deprecated" {
			continue
		}
		// Empty Kind means language (catalog convention). Web search needs a
		// language model that can call the hosted search tool.
		if m.Kind != "" && m.Kind != provider.KindLanguage {
			continue
		}
		if !m.ToolCall {
			continue
		}
		if containsAny(strings.ToLower(m.ID), searchExcludeTokens) {
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return ""
	}
	for _, tok := range tierTokens {
		var tiered []provider.ModelInfo
		for _, m := range candidates {
			if strings.Contains(strings.ToLower(m.ID), tok) {
				tiered = append(tiered, m)
			}
		}
		if len(tiered) > 0 {
			return pickBest(tiered)
		}
	}
	// No tier token matched — return "" so the caller uses its pinned constant
	// instead of silently substituting an off-tier model.
	return ""
}

// pickBest returns the id of the best model from a tier-matched set. Ranking:
// canonical aliases beat dated snapshots; then newest by release date; finally
// cheapest input cost. (Latency/reasoning intent is carried by the per-provider
// tier tokens — e.g. grok's "non-reasoning" — not a global rule, since for some
// providers "reasoning" is just a capability flag on the same model.)
func pickBest(models []provider.ModelInfo) string {
	sort.SliceStable(models, func(i, j int) bool {
		a, b := models[i], models[j]
		if da, db := datedSnapshot.MatchString(a.ID), datedSnapshot.MatchString(b.ID); da != db {
			return !da // non-dated (canonical alias) first
		}
		if a.ReleaseDate != b.ReleaseDate {
			return a.ReleaseDate > b.ReleaseDate // ISO dates sort lexically; newest first
		}
		return inputCost(a) < inputCost(b)
	})
	return models[0].ID
}

func containsAny(s string, toks []string) bool {
	for _, t := range toks {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

func inputCost(m provider.ModelInfo) float64 {
	if m.Cost == nil {
		return math.MaxFloat64
	}
	return m.Cost.Input
}
