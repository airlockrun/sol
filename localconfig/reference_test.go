package localconfig

import "testing"

func TestModelReference(t *testing.T) {
	for _, tc := range []struct {
		value, entry, model string
		valid               bool
	}{
		{"work/openai/gpt-5.4", "work/openai", "gpt-5.4", true},
		{"Personal_2/huggingface/org/model", "Personal_2/huggingface", "org/model", true},
		{"work.v2/openai/gpt", "work.v2/openai", "gpt", true},
		{"openai/gpt", "", "", false}, {"/openai/gpt", "", "", false},
		{"_work/openai/gpt", "", "", false}, {"../openai/gpt", "", "", false},
		{"work/openai/", "", "", false}, {"work/openai/gpt\n", "", "", false},
		{"work/openai/gpt\x00", "", "", false}, {"work/openai/gpt\u00a0", "", "", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			ref, err := ParseModelRef(tc.value)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if tc.valid && (ref.Entry != tc.entry || ref.Model != tc.model) {
				t.Fatal("model suffix or account lost")
			}
		})
	}
}
func TestDisconnectedAndExclusiveMethods(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		p           ProviderConfig
		valid       bool
	}{
		{"disconnected key", "work/openai", ProviderConfig{Auth: APIKeyMode}, true},
		{"disconnected codex", "work/openai", ProviderConfig{Auth: "codex"}, true},
		{"explicit env", "work/openai", ProviderConfig{Auth: APIKeyMode, KeyEnv: "WORK_KEY"}, true},
		{"ambiguous keys", "work/openai", ProviderConfig{Auth: APIKeyMode, Key: "test", KeyEnv: "WORK_KEY"}, false},
		{"wrong OAuth provider", "work/anthropic", ProviderConfig{Auth: "codex"}, false},
		{"mixed method", "work/openai", ProviderConfig{Auth: "codex", Key: "test"}, false},
		{"unknown method", "work/openai", ProviderConfig{Auth: "unknown"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Providers: map[string]ProviderConfig{tc.entry: tc.p}, DefaultModel: tc.entry + "/model"}
			if (c.Validate() == nil) != tc.valid {
				t.Fatal("wrong method validation")
			}
		})
	}
	c := Config{Providers: map[string]ProviderConfig{"Work/openai": {Auth: APIKeyMode}}}
	if _, _, err := c.Resolve("work/openai/gpt"); err == nil {
		t.Fatal("case-insensitive account collision")
	}
}
