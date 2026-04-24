package provider

import "testing"

func TestParseModel(t *testing.T) {
	tests := []struct {
		input     string
		wantProv  string
		wantModel string
	}{
		{"openai/gpt-4o-mini", "openai", "gpt-4o-mini"},
		{"anthropic/claude-3-5-sonnet", "anthropic", "claude-3-5-sonnet"},
		{"google/gemini-1.5-pro", "google", "gemini-1.5-pro"},
		{"gpt-4o", "openai", "gpt-4o"},                            // default to openai
		{"claude-3-5-sonnet", "openai", "claude-3-5-sonnet"},      // default to openai even for non-openai models
		{"azure/gpt-4o/2024-08-06", "azure", "gpt-4o/2024-08-06"}, // handles nested slashes
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotProv, gotModel := ParseModel(tt.input)
			if gotProv != tt.wantProv {
				t.Errorf("ParseModel(%q) provider = %q, want %q", tt.input, gotProv, tt.wantProv)
			}
			if gotModel != tt.wantModel {
				t.Errorf("ParseModel(%q) model = %q, want %q", tt.input, gotModel, tt.wantModel)
			}
		})
	}
}

func TestGetEnvVarName(t *testing.T) {
	tests := []struct {
		provider string
		wantAny  []string // accept any of these values (models.dev data may vary)
	}{
		{"openai", []string{"OPENAI_API_KEY"}},
		{"anthropic", []string{"ANTHROPIC_API_KEY"}},
		{"google", []string{"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"}}, // models.dev may use either
		{"unknown", []string{"UNKNOWN_API_KEY"}},                               // falls back to uppercased name
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := GetEnvVarName(tt.provider)
			found := false
			for _, want := range tt.wantAny {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("GetEnvVarName(%q) = %q, want one of %v", tt.provider, got, tt.wantAny)
			}
		})
	}
}

func TestGetDisplayName(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"openai", "OpenAI"},
		{"anthropic", "Anthropic"},
		{"google", "Google"},
		{"unknown", "unknown"}, // returns ID if not found
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := GetDisplayName(tt.provider)
			if got != tt.want {
				t.Errorf("GetDisplayName(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}
