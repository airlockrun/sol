package provider

import "testing"

func TestPolicyFor(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     AttachmentPolicy
	}{
		{
			name:     "openai: image URL only, 50 images, ≤18 MiB each",
			provider: "openai",
			model:    "gpt-4o",
			want: AttachmentPolicy{
				SupportsURL:         true,
				SupportsFileURL:     false,
				MaxURLImages:        50,
				MaxURLBytesPerImage: 18 * 1024 * 1024,
				MaxInlineBytesTotal: 40 * 1024 * 1024,
			},
		},
		{
			name:     "anthropic: image+file URL, enforces 5 MB/image hard cap",
			provider: "anthropic",
			model:    "claude-3-5-sonnet-20241022",
			want: AttachmentPolicy{
				SupportsURL:         true,
				SupportsFileURL:     true,
				MaxURLImages:        50,
				MaxURLBytesPerImage: 4*1024*1024 + 512*1024,
				MaxInlineBytesTotal: 24 * 1024 * 1024,
			},
		},
		{
			name:     "google: image+file URL, generous caps",
			provider: "google",
			model:    "gemini-2.0-flash",
			want: AttachmentPolicy{
				SupportsURL:         true,
				SupportsFileURL:     true,
				MaxURLImages:        100,
				MaxURLBytesPerImage: 20 * 1024 * 1024,
				MaxInlineBytesTotal: 80 * 1024 * 1024,
			},
		},
		{
			name:     "unknown provider falls back to inline default",
			provider: "deepseek",
			model:    "deepseek-chat",
			want:     DefaultAttachmentPolicy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PolicyFor(tt.provider, tt.model)
			if got != tt.want {
				t.Errorf("PolicyFor(%q, %q) = %+v, want %+v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
