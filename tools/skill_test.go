package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/airlockrun/goai/tool"
)

func TestSkillRegistry_ParseSkillFile(t *testing.T) {
	// Create temp directory with a skill file
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillContent := `---
name: test-skill
description: A test skill for unit testing
---

# Test Skill

This is the content of the test skill.

## Instructions

1. Do something
2. Do something else
`

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Parse the skill file
	registry := &SkillRegistry{skills: make(map[string]SkillInfo)}
	skill, err := registry.parseSkillFile(skillPath)
	if err != nil {
		t.Fatalf("parseSkillFile failed: %v", err)
	}

	if skill.Name != "test-skill" {
		t.Errorf("expected name 'test-skill', got %q", skill.Name)
	}
	if skill.Description != "A test skill for unit testing" {
		t.Errorf("expected description 'A test skill for unit testing', got %q", skill.Description)
	}
	if skill.Location != skillPath {
		t.Errorf("expected location %q, got %q", skillPath, skill.Location)
	}
}

func TestSkillRegistry_LoadContent(t *testing.T) {
	// Create temp directory with a skill file
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillContent := `---
name: test-skill
description: A test skill
---

# Test Skill Content

This is the actual content.`

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	registry := &SkillRegistry{skills: make(map[string]SkillInfo)}
	skill := SkillInfo{
		Name:     "test-skill",
		Location: skillPath,
	}

	content, err := registry.LoadContent(skill)
	if err != nil {
		t.Fatalf("LoadContent failed: %v", err)
	}

	expected := "# Test Skill Content\n\nThis is the actual content."
	if content != expected {
		t.Errorf("expected content:\n%s\n\ngot:\n%s", expected, content)
	}
}

func TestSkillRegistry_ScanDir(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()

	// Create .claude/skills/skill1/SKILL.md
	skill1Dir := filepath.Join(tmpDir, "skills", "skill1")
	if err := os.MkdirAll(skill1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	skill1Content := `---
name: skill1
description: First skill
---

Content 1`
	if err := os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(skill1Content), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .claude/skills/skill2/SKILL.md
	skill2Dir := filepath.Join(tmpDir, "skills", "skill2")
	if err := os.MkdirAll(skill2Dir, 0755); err != nil {
		t.Fatal(err)
	}
	skill2Content := `---
name: skill2
description: Second skill
---

Content 2`
	if err := os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(skill2Content), 0644); err != nil {
		t.Fatal(err)
	}

	// Scan the directory
	registry := &SkillRegistry{skills: make(map[string]SkillInfo)}
	if err := registry.scanDir(tmpDir); err != nil {
		t.Fatalf("scanDir failed: %v", err)
	}

	if len(registry.skills) != 2 {
		t.Errorf("expected 2 skills, got %d", len(registry.skills))
	}

	if _, ok := registry.skills["skill1"]; !ok {
		t.Error("skill1 not found")
	}
	if _, ok := registry.skills["skill2"]; !ok {
		t.Error("skill2 not found")
	}
}

func TestSkillTool_Execute(t *testing.T) {
	// Create temp directory with a skill
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillContent := `---
name: my-skill
description: My awesome skill
---

# My Skill

Do awesome things.`

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Reset and configure skill registry for test
	skillRegistry = &SkillRegistry{
		skills:   make(map[string]SkillInfo),
		scanDirs: []string{tmpDir},
	}

	// Force scan
	_ = skillRegistry.Scan()

	// Create skill tool and execute
	skillTool := Skill()

	input := SkillInput{Name: "my-skill"}
	inputJSON, _ := json.Marshal(input)

	result, err := skillTool.Execute(context.Background(), inputJSON, tool.CallOptions{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Title != "Loaded skill: my-skill" {
		t.Errorf("expected title 'Loaded skill: my-skill', got %q", result.Title)
	}

	// Check output contains expected parts
	if !contains(result.Output, "## Skill: my-skill") {
		t.Errorf("output missing skill header")
	}
	if !contains(result.Output, "**Base directory**:") {
		t.Errorf("output missing base directory")
	}
	if !contains(result.Output, "# My Skill") {
		t.Errorf("output missing skill content")
	}
}

func TestSkillTool_NotFound(t *testing.T) {
	// Reset skill registry with no skills
	skillRegistry = &SkillRegistry{
		skills:   make(map[string]SkillInfo),
		scanDirs: []string{}, // Empty dirs means no skills
		scanned:  true,       // Mark as scanned
	}

	skillTool := Skill()

	input := SkillInput{Name: "nonexistent"}
	inputJSON, _ := json.Marshal(input)

	_, err := skillTool.Execute(context.Background(), inputJSON, tool.CallOptions{})
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}

	if !contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestBuildSkillDescription(t *testing.T) {
	// Test with no skills
	skillRegistry = &SkillRegistry{
		skills:  make(map[string]SkillInfo),
		scanned: true,
	}

	desc := buildSkillDescription()
	if !contains(desc, "No skills are currently available") {
		t.Errorf("expected 'No skills are currently available' in description, got: %s", desc)
	}

	// Test with skills
	skillRegistry = &SkillRegistry{
		skills: map[string]SkillInfo{
			"test": {Name: "test", Description: "Test skill"},
		},
		scanned: true,
	}

	desc = buildSkillDescription()
	if !contains(desc, "<available_skills>") {
		t.Errorf("expected '<available_skills>' in description, got: %s", desc)
	}
	if !contains(desc, "<name>test</name>") {
		t.Errorf("expected skill name in description, got: %s", desc)
	}
	if !contains(desc, "<description>Test skill</description>") {
		t.Errorf("expected skill description in description, got: %s", desc)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || strings.Contains(s, substr))
}
