package sol

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Provider-specific prompts (matching opencode's session/prompt/ files)
//
//go:embed prompt/beast.txt
var beastPrompt string

//go:embed prompt/anthropic.txt
var anthropicPrompt string

//go:embed prompt/gemini.txt
var geminiPrompt string

//go:embed prompt/qwen.txt
var qwenPrompt string

//go:embed prompt/codex_header.txt
var codexPrompt string

//go:embed prompt/copilot-gpt-5.txt
var copilotGpt5Prompt string

// Other prompts
//
//go:embed prompt/title.txt
var titlePrompt string

//go:embed prompt/plan.txt
var planPrompt string

//go:embed prompt/plan-reminder-anthropic.txt
var planReminderAnthropicPrompt string

//go:embed prompt/build-switch.txt
var buildSwitchPrompt string

//go:embed prompt/max-steps.txt
var maxStepsPrompt string

// selectProviderPrompt returns the appropriate base prompt for the given model.
// This matches opencode's SystemPrompt.provider() logic in session/system.ts:
//
//	if (model.api.id.includes("gpt-5")) return [PROMPT_CODEX]
//	if (model.api.id.includes("gpt-") || model.api.id.includes("o1") || model.api.id.includes("o3")) return [PROMPT_BEAST]
//	if (model.api.id.includes("gemini-")) return [PROMPT_GEMINI]
//	if (model.api.id.includes("claude")) return [PROMPT_ANTHROPIC]
//	return [PROMPT_ANTHROPIC_WITHOUT_TODO]  // qwen.txt
func selectProviderPrompt(modelID string) string {
	id := strings.ToLower(modelID)

	// GPT-5 models use codex prompt
	if strings.Contains(id, "gpt-5") {
		// Check for copilot specifically
		if strings.Contains(id, "copilot") {
			return copilotGpt5Prompt
		}
		return codexPrompt
	}

	// OpenAI GPT-4, o1, o3 models use beast prompt
	if strings.Contains(id, "gpt-") || strings.Contains(id, "o1") || strings.Contains(id, "o3") {
		return beastPrompt
	}

	if strings.Contains(id, "gemini") {
		return geminiPrompt
	}

	if strings.Contains(id, "claude") {
		return anthropicPrompt
	}

	return qwenPrompt
}

// SystemPrompt generates the system prompt for the agent.
// It selects the appropriate provider-specific prompt and appends environment info.
// modelID is the model identifier (e.g., "gpt-4o"), workDir is the working directory.
func SystemPrompt(modelID, workDir string) string {
	basePrompt := selectProviderPrompt(modelID)

	// Detect git info from workDir
	isGitRepo := "no"
	gitDir := filepath.Join(workDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		isGitRepo = "yes"
	}

	// Parse provider from modelID for display
	providerID := "openai"
	if strings.Contains(modelID, "claude") {
		providerID = "anthropic"
	} else if strings.Contains(modelID, "gemini") {
		providerID = "google"
	}

	modelInfo := fmt.Sprintf("You are powered by the model named %s. The exact model ID is %s/%s", modelID, providerID, modelID)

	env := fmt.Sprintf("\n%s\nHere is some useful information about the environment you are running in:\n<env>\n  Working directory: %s\n  Is directory a git repo: %s\n  Platform: %s\n  Today's date: %s\n</env>\n<files>\n  \n</files>", modelInfo, workDir, isGitRepo, runtime.GOOS, time.Now().Format("Mon Jan 02 2006"))

	return basePrompt + env
}

// EnvironmentInfo returns environment information for agents with custom prompts.
func EnvironmentInfo(workDir string) string {
	isGitRepo := "No"
	gitDir := filepath.Join(workDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		isGitRepo = "Yes"
	}

	return fmt.Sprintf(`<env>
Working directory: %s
Is directory a git repo: %s
Platform: %s
Today's date: %s
</env>`, workDir, isGitRepo, runtime.GOOS, time.Now().Format("2006-01-02"))
}

// TitlePrompt returns the title generation prompt.
func TitlePrompt() string {
	return titlePrompt
}

// PlanPrompt returns the plan mode prompt.
func PlanPrompt() string {
	return planPrompt
}

// PlanReminderAnthropicPrompt returns the Anthropic-specific plan reminder.
func PlanReminderAnthropicPrompt() string {
	return planReminderAnthropicPrompt
}

// BuildSwitchPrompt returns the build switch prompt.
func BuildSwitchPrompt() string {
	return buildSwitchPrompt
}

// MaxStepsPrompt returns the max steps warning prompt.
func MaxStepsPrompt() string {
	return maxStepsPrompt
}

// GetPromptForModel returns the provider-specific prompt name for logging/debugging.
func GetPromptForModel(modelID string) string {
	id := strings.ToLower(modelID)

	if strings.Contains(id, "gpt-5") {
		if strings.Contains(id, "copilot") {
			return "copilot-gpt-5"
		}
		return "codex"
	}
	if strings.Contains(id, "gpt-") || strings.Contains(id, "o1") || strings.Contains(id, "o3") {
		return "beast"
	}
	if strings.Contains(id, "gemini") {
		return "gemini"
	}
	if strings.Contains(id, "claude") {
		return "anthropic"
	}
	return "qwen"
}
