package agent

import (
	_ "embed"
	"os"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/tools"
)

// Built-in agent names.
const (
	AgentBuild      = "build"
	AgentPlan       = "plan"
	AgentExplore    = "explore"
	AgentGeneral    = "general"
	AgentCompaction = "compaction"
)

//go:embed prompt/explore.txt
var explorePrompt string

//go:embed prompt/compaction.txt
var compactionPrompt string

// NewBuildAgent creates the default full-access agent for software engineering tasks.
// Uses the model-specific default prompt (SystemPrompt is empty).
func NewBuildAgent(modelID string) *Agent {
	return &Agent{
		Name:     AgentBuild,
		Tools:    tools.CreateToolSetForModel(modelID),
		MaxSteps: 50,
	}
}

// NewPlanAgent creates a read-only agent for planning and design.
func NewPlanAgent(modelID string) *Agent {
	t := make(tool.Set)
	t.Add(tools.Read())
	t.Add(tools.Glob())
	t.Add(tools.Grep())
	return &Agent{
		Name:     AgentPlan,
		Tools:    t,
		MaxSteps: 30,
	}
}

// NewExploreAgent creates a specialized subagent for codebase exploration.
func NewExploreAgent(modelID string) *Agent {
	t := make(tool.Set)
	t.Add(tools.Read())
	t.Add(tools.Glob())
	t.Add(tools.Grep())
	wd, _ := os.Getwd()
	t.Add(tools.Bash(wd))
	t.Add(tools.Webfetch())
	return &Agent{
		Name:         AgentExplore,
		SystemPrompt: explorePrompt,
		Tools:        t,
		MaxSteps:     20,
	}
}

// NewGeneralAgent creates a general-purpose subagent for delegated tasks.
func NewGeneralAgent(modelID string) *Agent {
	t := tools.CreateToolSetForModel(modelID)
	delete(t, "todoread")
	delete(t, "todowrite")
	return &Agent{
		Name:     AgentGeneral,
		Tools:    t,
		MaxSteps: 30,
	}
}

// NewCompactionAgent creates a hidden agent for context compaction.
func NewCompactionAgent(_ string) *Agent {
	return &Agent{
		Name:         AgentCompaction,
		SystemPrompt: compactionPrompt,
		Tools:        make(tool.Set),
		MaxSteps:     1,
	}
}

// init registers the built-in agents with the default registry.
func init() {
	MustRegister(AgentBuild, NewBuildAgent)
	MustRegister(AgentPlan, NewPlanAgent)
	MustRegister(AgentExplore, NewExploreAgent)
	MustRegister(AgentGeneral, NewGeneralAgent)
	MustRegister(AgentCompaction, NewCompactionAgent)
}
