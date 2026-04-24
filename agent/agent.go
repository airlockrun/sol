// Package agent provides the agent system for Sol, enabling multiple specialized
// agents with different capabilities and system prompts.
package agent

import (
	"github.com/airlockrun/goai/tool"
)

// Agent defines an agent configuration — what model, tools, and prompt to use.
type Agent struct {
	// Name is the unique identifier for this agent (e.g., "build", "explore").
	Name string

	// Model is the full provider/model string (e.g., "anthropic/claude-sonnet-4-20250514").
	// Parsed internally via provider.ParseModel().
	Model string

	// SystemPrompt is the base system prompt for this agent.
	// Empty means use the model-specific default prompt.
	SystemPrompt string

	// EnvironmentPrompt is appended to the system prompt with environment context.
	// If empty, Sol appends default environment info (workDir, platform, date).
	// Set this to override with custom context (e.g. conversation ID, platform type).
	EnvironmentPrompt string

	// Tools is the exact set of tools this agent can use. No filtering.
	Tools tool.Set

	// MaxSteps limits how many steps this agent can take (0 = default 50).
	MaxSteps int

	// Temperature sets the model temperature for this agent (nil = provider default).
	Temperature *float64

	// HistoryPolicy controls which message parts from conversation history are
	// included when building the LLM context. Zero value includes everything
	// (matching opencode behavior).
	HistoryPolicy HistoryPolicy
}

// HistoryPolicy controls which message parts from conversation history are
// included when building the LLM context. Zero value includes everything.
type HistoryPolicy struct {
	ExcludeReasoning bool // Strip ReasoningParts from assistant messages
	ExcludeToolCalls bool // Strip ToolCallParts + skip tool-role messages
	ExcludeFiles     bool // Strip ImageParts/FileParts from all messages — stripped at PERSIST time, never reach history

	// FilesRetainTurns keeps image/file parts in the N most recent user turns
	// of history, then strips them when loading older messages. 0 disables
	// (nothing stripped at load time). Only meaningful when ExcludeFiles=false
	// — with ExcludeFiles=true files are already gone before they reach history.
	FilesRetainTurns int
}
