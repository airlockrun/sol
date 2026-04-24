// Package tools provides the tool implementations for Sol.
package tools

import (
	"os"
	"strings"

	"github.com/airlockrun/goai/tool"
)

// Re-export context keys from goai/tool for backward compatibility.
// Tools should use these keys for context.Value() lookups.
const (
	RunnerKey    = tool.RunnerKey
	SessionIDKey = tool.SessionIDKey
	WorkDirKey   = tool.WorkDirKey
)

// CreateAllTools returns a tool.Set containing every tool implementation.
// workDir is used in the bash tool description to tell the LLM the default
// working directory. Pass "" to use os.Getwd().
func CreateAllTools(workDir string) tool.Set {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	tools := make(tool.Set)
	tools.Add(Question())
	tools.Add(Bash(workDir))
	tools.Add(Read())
	tools.Add(Glob())
	tools.Add(Grep())
	tools.Add(Edit())
	tools.Add(Write())
	tools.Add(ApplyPatch())
	tools.Add(Task())
	tools.Add(Webfetch())
	tools.Add(TodoWrite())
	tools.Add(TodoRead())
	tools.Add(Skill())
	return tools
}

// CreateToolSetForModel creates tools matching OpenCode's model-based tool selection.
// For gpt-5+ models (except gpt-4*), uses apply_patch instead of edit/write.
// CreateToolSetForModel creates tools matching OpenCode's model-based tool selection.
// For gpt-5+ models (except gpt-4*), uses apply_patch instead of edit/write.
func CreateToolSetForModel(modelID string) tool.Set {
	workDir, _ := os.Getwd()
	tools := make(tool.Set)

	// Determine if we should use the patch tool (codex-style)
	// Logic from opencode: use patch for gpt-* models except gpt-4* and *-oss
	usePatch := strings.Contains(modelID, "gpt-") &&
		!strings.Contains(modelID, "oss") &&
		!strings.Contains(modelID, "gpt-4")

	// Tools in opencode's exact order
	tools.Add(Question())
	tools.Add(Bash(workDir))
	tools.Add(Read())
	tools.Add(Glob())
	tools.Add(Grep())

	if usePatch {
		// gpt-5+ models use apply_patch (codex-style)
		tools.Add(Task())
		tools.Add(Webfetch())
		tools.Add(TodoWrite())
		tools.Add(TodoRead())
		tools.Add(Skill())
		tools.Add(ApplyPatch())
	} else {
		// Other models use edit + write
		tools.Add(Edit())
		tools.Add(Write())
		tools.Add(Task())
		tools.Add(Webfetch())
		tools.Add(TodoWrite())
		tools.Add(TodoRead())
		tools.Add(Skill())
	}

	return tools
}

// MergeToolSets merges multiple tool sets into one.
// Later sets override earlier ones if there are name conflicts.
func MergeToolSets(sets ...tool.Set) tool.Set {
	merged := make(tool.Set)
	for _, s := range sets {
		for name, t := range s {
			merged[name] = t
		}
	}
	return merged
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
