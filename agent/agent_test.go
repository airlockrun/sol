package agent

import (
	"testing"
)

func TestAgent_Struct(t *testing.T) {
	temp := 0.7
	a := &Agent{
		Name:         "test",
		Model:        "openai/gpt-4o",
		SystemPrompt: "test prompt",
		MaxSteps:     10,
		Temperature:  &temp,
	}

	if a.Name != "test" {
		t.Errorf("Name = %s", a.Name)
	}
	if a.Model != "openai/gpt-4o" {
		t.Errorf("Model = %s", a.Model)
	}
	if a.MaxSteps != 10 {
		t.Errorf("MaxSteps = %d", a.MaxSteps)
	}
	if *a.Temperature != 0.7 {
		t.Errorf("Temperature = %f", *a.Temperature)
	}
}

func TestNewBuildAgent_HasAllTools(t *testing.T) {
	a := NewBuildAgent("gpt-4o")
	if a.Name != AgentBuild {
		t.Errorf("Name = %s, want %s", a.Name, AgentBuild)
	}
	// Build agent should have core tools
	for _, name := range []string{"read", "glob", "grep", "write", "edit", "bash", "task"} {
		if _, ok := a.Tools[name]; !ok {
			t.Errorf("build agent missing tool %s", name)
		}
	}
}

func TestNewExploreAgent_HasLimitedTools(t *testing.T) {
	a := NewExploreAgent("gpt-4o")
	if a.Name != AgentExplore {
		t.Errorf("Name = %s, want %s", a.Name, AgentExplore)
	}
	// Explore agent should have read, glob, grep, bash, webfetch
	for _, name := range []string{"read", "glob", "grep", "bash", "webfetch"} {
		if _, ok := a.Tools[name]; !ok {
			t.Errorf("explore agent missing tool %s", name)
		}
	}
	// Explore agent should NOT have write, edit, task
	for _, name := range []string{"write", "edit", "task"} {
		if _, ok := a.Tools[name]; ok {
			t.Errorf("explore agent should not have %s tool", name)
		}
	}
	if a.SystemPrompt == "" {
		t.Error("explore agent should have a custom system prompt")
	}
}

func TestNewPlanAgent_HasReadOnlyTools(t *testing.T) {
	a := NewPlanAgent("gpt-4o")
	if a.Name != AgentPlan {
		t.Errorf("Name = %s, want %s", a.Name, AgentPlan)
	}
	// Plan agent should have read tools only
	for _, name := range []string{"read", "glob", "grep"} {
		if _, ok := a.Tools[name]; !ok {
			t.Errorf("plan agent missing tool %s", name)
		}
	}
	// Plan agent should NOT have write, edit, bash, task
	for _, name := range []string{"write", "edit", "bash", "task"} {
		if _, ok := a.Tools[name]; ok {
			t.Errorf("plan agent should not have %s tool", name)
		}
	}
}

func TestNewGeneralAgent_DeniesTodo(t *testing.T) {
	a := NewGeneralAgent("gpt-4o")
	if a.Name != AgentGeneral {
		t.Errorf("Name = %s, want %s", a.Name, AgentGeneral)
	}
	// General should have bash, read, write, edit, task
	for _, name := range []string{"read", "write", "edit", "bash", "task"} {
		if _, ok := a.Tools[name]; !ok {
			t.Errorf("general agent missing tool %s", name)
		}
	}
	// General should NOT have todoread, todowrite
	for _, name := range []string{"todoread", "todowrite"} {
		if _, ok := a.Tools[name]; ok {
			t.Errorf("general agent should not have %s tool", name)
		}
	}
}

func TestNewCompactionAgent_NoTools(t *testing.T) {
	a := NewCompactionAgent("")
	if a.Name != AgentCompaction {
		t.Errorf("Name = %s, want %s", a.Name, AgentCompaction)
	}
	if len(a.Tools) != 0 {
		t.Errorf("compaction agent should have 0 tools, got %d", len(a.Tools))
	}
	if a.MaxSteps != 1 {
		t.Errorf("compaction agent MaxSteps = %d, want 1", a.MaxSteps)
	}
	if a.SystemPrompt == "" {
		t.Error("compaction agent should have a custom system prompt")
	}
}
