package agent

import (
	"testing"

	"github.com/airlockrun/goai/tool"
)

func TestRegistry_Register(t *testing.T) {
	registry := NewRegistry()

	factory := func(modelID string) *Agent {
		return &Agent{Name: "test", MaxSteps: 10}
	}
	err := registry.Register("test", factory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Duplicate registration should fail
	err = registry.Register("test", factory)
	if err == nil {
		t.Error("expected error for duplicate registration")
	}

	// Empty name should fail
	err = registry.Register("", factory)
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestRegistry_Get(t *testing.T) {
	registry := NewRegistry()

	registry.MustRegister("test", func(modelID string) *Agent {
		return &Agent{Name: "test", MaxSteps: 10}
	})

	// Get existing agent
	retrieved, exists := registry.Get("test", "gpt-4o")
	if !exists {
		t.Fatal("expected agent to exist")
	}
	if retrieved.Name != "test" {
		t.Errorf("expected name 'test', got '%s'", retrieved.Name)
	}
	if retrieved.MaxSteps != 10 {
		t.Errorf("expected MaxSteps 10, got %d", retrieved.MaxSteps)
	}

	// Get returns a fresh agent each time
	retrieved.MaxSteps = 99
	original, _ := registry.Get("test", "gpt-4o")
	if original.MaxSteps != 10 {
		t.Error("Get should return a fresh agent each time")
	}

	// Get non-existing agent
	_, exists = registry.Get("nonexistent", "gpt-4o")
	if exists {
		t.Error("expected agent to not exist")
	}
}

func TestRegistry_GetFactory(t *testing.T) {
	registry := NewRegistry()

	registry.MustRegister("test", func(modelID string) *Agent {
		return &Agent{Name: "test", Tools: tool.Set{"model": tool.Tool{Name: modelID}}}
	})

	factory, exists := registry.GetFactory("test")
	if !exists {
		t.Fatal("expected factory to exist")
	}

	a := factory("gpt-4o")
	if a.Name != "test" {
		t.Errorf("Name = %s", a.Name)
	}
	if _, ok := a.Tools["model"]; !ok {
		t.Error("factory should pass modelID through")
	}

	_, exists = registry.GetFactory("nonexistent")
	if exists {
		t.Error("expected factory to not exist")
	}
}

func TestRegistry_List(t *testing.T) {
	registry := NewRegistry()

	registry.MustRegister("agent1", func(modelID string) *Agent { return &Agent{Name: "agent1"} })
	registry.MustRegister("agent2", func(modelID string) *Agent { return &Agent{Name: "agent2"} })
	registry.MustRegister("agent3", func(modelID string) *Agent { return &Agent{Name: "agent3"} })

	names := registry.List()
	if len(names) != 3 {
		t.Errorf("expected 3 agents, got %d", len(names))
	}

	nameSet := make(map[string]bool)
	for _, name := range names {
		nameSet[name] = true
	}
	for _, expected := range []string{"agent1", "agent2", "agent3"} {
		if !nameSet[expected] {
			t.Errorf("expected %s in list", expected)
		}
	}
}

func TestDefaultRegistry_BuiltinAgents(t *testing.T) {
	names := List()
	if len(names) < 4 {
		t.Errorf("expected at least 4 built-in agents, got %d", len(names))
	}

	for _, name := range []string{AgentBuild, AgentPlan, AgentExplore, AgentGeneral} {
		a, exists := Get(name, "gpt-4o")
		if !exists {
			t.Errorf("expected built-in agent %s to exist", name)
			continue
		}
		if a.Name != name {
			t.Errorf("expected agent name %s, got %s", name, a.Name)
		}
	}
}

func TestBuiltinAgents_ToolSets(t *testing.T) {
	build, _ := Get(AgentBuild, "gpt-4o")
	for _, name := range []string{"read", "write", "edit", "bash", "task"} {
		if _, ok := build.Tools[name]; !ok {
			t.Errorf("build agent missing tool %s", name)
		}
	}

	plan, _ := Get(AgentPlan, "gpt-4o")
	if _, ok := plan.Tools["read"]; !ok {
		t.Error("plan agent missing read tool")
	}
	if _, ok := plan.Tools["write"]; ok {
		t.Error("plan agent should not have write tool")
	}

	explore, _ := Get(AgentExplore, "gpt-4o")
	if _, ok := explore.Tools["read"]; !ok {
		t.Error("explore agent missing read tool")
	}
	if _, ok := explore.Tools["bash"]; !ok {
		t.Error("explore agent missing bash tool")
	}
	if _, ok := explore.Tools["write"]; ok {
		t.Error("explore agent should not have write tool")
	}

	general, _ := Get(AgentGeneral, "gpt-4o")
	if _, ok := general.Tools["bash"]; !ok {
		t.Error("general agent missing bash tool")
	}
	if _, ok := general.Tools["todoread"]; ok {
		t.Error("general agent should not have todoread tool")
	}
	if _, ok := general.Tools["todowrite"]; ok {
		t.Error("general agent should not have todowrite tool")
	}
}
