package provider

// AttachmentPolicy describes how a given provider/model accepts image and
// file attachments. Used by airlock's attachref resolver to decide between
// URL mode (presigned S3 link) and inline mode (base64-encoded bytes), and
// to enforce per-request size caps.
//
// Defaults are conservative: inline-only, 5 MiB per-request budget. Provider
// overrides (see AttachmentOverlay) relax these where the upstream API is
// known to support URL references.
type AttachmentPolicy struct {
	// MaxInlineBytesTotal caps the total base64-encoded attachment bytes in
	// a single request body. Parts that don't fit are evicted (oldest first)
	// and replaced with a text placeholder that tells the LLM how to
	// re-attach.
	MaxInlineBytesTotal int

	// SupportsURL is true when the provider accepts http(s):// URLs for
	// image parts. OpenAI chat+responses detect the prefix on ImagePart.Image;
	// Anthropic + Google do the same and also accept it via FilePart.URL (see
	// SupportsFileURL).
	SupportsURL bool

	// SupportsFileURL is true when the provider accepts http(s):// URLs for
	// file parts via message.FilePart.URL. Anthropic + Google support PDFs
	// and other file types by URL; OpenAI currently does not on the chat
	// path.
	SupportsFileURL bool

	// MaxURLImages caps how many image/file parts per request can be sent
	// as URLs before we fall back to inline. Shared across both kinds to
	// keep the provider from rejecting oversized fan-out fetches.
	MaxURLImages int

	// MaxURLBytesPerImage is the per-object size ceiling when sending URLs.
	// Objects above this are fetched and inlined (or evicted) rather than
	// referenced.
	MaxURLBytesPerImage int
}

// DefaultAttachmentPolicy is the fallback — inline-only, 5 MiB request cap.
var DefaultAttachmentPolicy = AttachmentPolicy{
	MaxInlineBytesTotal: 5 * 1024 * 1024,
	SupportsURL:         false,
}

// AttachmentOverlay is the per-provider override map. Keyed by provider_id
// (same as Overlay above). Values are applied as-is — not merged with
// DefaultAttachmentPolicy — so each entry must be complete.
//
// Covered: every provider in goai whose message conversion detects the
// http(s) prefix on message.ImagePart.Image or reads message.FilePart.URL.
//
// Sizing references (as of 2026-Q2 docs):
//   - OpenAI GPT-4o: 500 images/req, 50 MB total payload. Per-image soft
//     cap ~20 MB (data-URI path). https://platform.openai.com/docs/guides/images-vision
//   - Anthropic Claude: 600 images/req, 32 MB request, HARD 5 MB per image
//     (URL or inline — provider enforces both paths).
//     https://platform.claude.com/docs/en/build-with-claude/vision
//   - Google Gemini: 3600 images/req, 100 MB URL-fetch per payload.
//     https://ai.google.dev/gemini-api/docs/image-understanding
//   - xAI Grok: no documented image count cap, 20 MiB per image.
//     https://docs.x.ai/docs/guides/image-understanding
//
// Our caps sit *below* each provider's hard limit to leave headroom for
// JSON framing, tool schemas, prompt bytes, etc. `MaxInlineBytesTotal` is
// the base64-encoded byte budget (roughly 1.33× the raw byte count).
var AttachmentOverlay = map[string]AttachmentPolicy{
	"openai": {
		SupportsURL:         true,
		SupportsFileURL:     false, // chat PDF path is base64-only
		MaxURLImages:        50,
		MaxURLBytesPerImage: 18 * 1024 * 1024, // under ~20 MB OpenAI soft cap
		MaxInlineBytesTotal: 40 * 1024 * 1024, // under 50 MB payload cap
	},
	"anthropic": {
		SupportsURL:         true,
		SupportsFileURL:     true,
		MaxURLImages:        50,
		MaxURLBytesPerImage: 4*1024*1024 + 512*1024, // 4.5 MiB — under 5 MB hard cap
		MaxInlineBytesTotal: 24 * 1024 * 1024,       // ≈ 32 MB / 1.33 base64 inflation
	},
	"google": {
		SupportsURL:         true,
		SupportsFileURL:     true,
		MaxURLImages:        100,
		MaxURLBytesPerImage: 20 * 1024 * 1024,
		MaxInlineBytesTotal: 80 * 1024 * 1024, // under 100 MB URL/inline cap
	},
	"vertex": { // Vertex delegates to Google's Gemini types
		SupportsURL:         true,
		SupportsFileURL:     true,
		MaxURLImages:        100,
		MaxURLBytesPerImage: 20 * 1024 * 1024,
		MaxInlineBytesTotal: 80 * 1024 * 1024,
	},
	"bedrock": { // Bedrock's Anthropic backend inherits Anthropic's caps
		SupportsURL:         true,
		SupportsFileURL:     true,
		MaxURLImages:        50,
		MaxURLBytesPerImage: 4*1024*1024 + 512*1024,
		MaxInlineBytesTotal: 24 * 1024 * 1024,
	},
	"xai": {
		SupportsURL:         true,
		SupportsFileURL:     false,
		MaxURLImages:        50,
		MaxURLBytesPerImage: 18 * 1024 * 1024, // under 20 MiB per-image
		MaxInlineBytesTotal: 40 * 1024 * 1024,
	},
	"openaicompat": { // generic proxies — keep conservative, unknown backend
		SupportsURL:         true,
		SupportsFileURL:     false,
		MaxURLImages:        5,
		MaxURLBytesPerImage: 3*1024*1024 + 512*1024,
		MaxInlineBytesTotal: 5 * 1024 * 1024,
	},
}

// PolicyFor returns the attachment policy for a given provider/model pair.
// modelID is accepted for future model-level overrides but currently unused.
func PolicyFor(providerID, modelID string) AttachmentPolicy {
	if p, ok := AttachmentOverlay[providerID]; ok {
		return p
	}
	return DefaultAttachmentPolicy
}
