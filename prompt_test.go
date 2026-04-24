package sol

import (
	"strings"
	"testing"
)

func TestGetPromptForModel(t *testing.T) {
	tests := []struct {
		modelID  string
		wantName string
	}{
		// OpenAI GPT-4 models -> beast
		{"gpt-4o", "beast"},
		{"gpt-4o-mini", "beast"},
		{"gpt-4-turbo", "beast"},

		// OpenAI o1/o3 models -> beast
		{"o1-preview", "beast"},
		{"o1-mini", "beast"},
		{"o3-mini", "beast"},

		// GPT-5 models -> codex
		{"gpt-5", "codex"},
		{"gpt-5-turbo", "codex"},
		{"gpt-5-nano", "codex"},

		// Gemini models -> gemini
		{"gemini-1.5-pro", "gemini"},
		{"gemini-2.0-flash", "gemini"},

		// Claude models -> anthropic
		{"claude-3-5-sonnet", "anthropic"},
		{"claude-3-opus", "anthropic"},
		{"claude-sonnet-4", "anthropic"},

		// Unknown/other models -> qwen (fallback)
		{"qwen-72b", "qwen"},
		{"llama-3-70b", "qwen"},
		{"mistral-large", "qwen"},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			name := GetPromptForModel(tt.modelID)
			if name != tt.wantName {
				t.Errorf("GetPromptForModel(%q) = %q, want %q", tt.modelID, name, tt.wantName)
			}
		})
	}
}

func TestSelectProviderPromptNotEmpty(t *testing.T) {
	models := []string{
		"gpt-4o", "gpt-5", "gemini-1.5-pro", "claude-3-5-sonnet", "qwen-72b",
	}

	for _, modelID := range models {
		t.Run(modelID, func(t *testing.T) {
			prompt := selectProviderPrompt(modelID)
			if prompt == "" {
				t.Errorf("selectProviderPrompt(%q) returned empty prompt", modelID)
			}
			if len(prompt) < 100 {
				t.Errorf("selectProviderPrompt(%q) returned suspiciously short prompt (%d chars)", modelID, len(prompt))
			}
		})
	}
}

func TestSystemPromptIncludesEnvInfo(t *testing.T) {
	prompt := SystemPrompt("gpt-4o-mini", "/tmp/test")

	checks := []string{
		"Working directory: /tmp/test",
		"gpt-4o-mini",
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("SystemPrompt missing %q", check)
		}
	}
}

func TestPromptSelectionCaseInsensitive(t *testing.T) {
	tests := []struct {
		modelID  string
		wantName string
	}{
		{"GPT-4O", "beast"},
		{"Gpt-4o", "beast"},
		{"CLAUDE-3-5-SONNET", "anthropic"},
		{"Claude-Sonnet-4", "anthropic"},
		{"GEMINI-1.5-PRO", "gemini"},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			name := GetPromptForModel(tt.modelID)
			if name != tt.wantName {
				t.Errorf("GetPromptForModel(%q) = %q, want %q", tt.modelID, name, tt.wantName)
			}
		})
	}
}
