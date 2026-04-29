package provider

import (
	"strings"
	"testing"
)

// TestLiveModelsDevParse exercises the real models.dev API end-to-end and
// guards against upstream changing a wire shape we have a typed field
// for. We've been bitten by this twice (e.g. `experimental: bool` →
// object), and the failure mode is silent: LoadProviders falls back to
// the empty builtin set, capabilities silently drop, vision pickers
// empty out, etc. — all without an obvious error.
//
// The test is skipped in `-short` mode and when the network is
// unreachable so offline / sandboxed runs (CI without egress) don't
// fail. When it does run, it asserts:
//
//   - fetchFromModelsDev returns no error (the actual parse check)
//   - common providers are present
//   - at least some openai language models declare image input
//     modality (a coarse vision-capability sanity check)
//
// If this test starts failing locally on a machine with network access,
// run `go test -run TestLiveModelsDevParse -v` and diff the live JSON
// against ModelInfo / ModelsDevProvider — upstream probably changed a
// field's type. Don't band-aid by removing the field; switch the
// existing one to json.RawMessage (see Experimental for precedent).
func TestLiveModelsDevParse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live models.dev fetch in -short mode")
	}

	providers, err := fetchFromModelsDev()
	if err != nil {
		// Network failures (DNS, transient outage) skip cleanly; an
		// unmarshal error fails — that's the regression we're guarding
		// against.
		if isNetworkErr(err) {
			t.Skipf("network unavailable, skipping: %v", err)
		}
		t.Fatalf("fetchFromModelsDev parse failed — upstream likely changed a wire shape: %v", err)
	}

	// Sanity: critical providers are present.
	for _, id := range []string{"openai", "anthropic", "google"} {
		if _, ok := providers[id]; !ok {
			t.Errorf("live models.dev missing provider %q", id)
		}
	}

	// Sanity: at least some openai language models declare image input.
	// If this becomes false the modality field has likely changed shape
	// (e.g. flat → nested), which would silently break the vision
	// capability inference.
	openai, ok := providers["openai"]
	if !ok {
		return // already errored above
	}
	visionFound := false
	for _, m := range openai.Models {
		if m.Modalities != nil && containsStr(m.Modalities.Input, "image") {
			visionFound = true
			break
		}
	}
	if !visionFound {
		t.Error("live models.dev: no openai model declares image input — modality wire shape may have changed")
	}
}

// isNetworkErr returns true for errors that look like the test machine
// can't reach the internet (DNS / connection refused / timeout). Match
// against the wrapped error message rather than typing every transport
// error variant — the heuristic just needs to be good enough to skip
// offline runs without false-failing.
func isNetworkErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, hint := range []string{
		"no such host",
		"connection refused",
		"network is unreachable",
		"i/o timeout",
		"dial tcp",
	} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}
