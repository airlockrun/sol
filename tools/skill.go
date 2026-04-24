package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/airlockrun/goai/tool"
)

// SkillInfo represents a discovered skill
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
}

// SkillInput is the input schema for the skill tool
type SkillInput struct {
	Name string `json:"name"`
}

// skillRegistry manages skill discovery and caching
var skillRegistry = &SkillRegistry{
	skills: make(map[string]SkillInfo),
}

// SkillRegistry manages skill discovery
type SkillRegistry struct {
	mu       sync.RWMutex
	skills   map[string]SkillInfo
	scanned  bool
	scanDirs []string // directories to scan for skills
}

// SetScanDirs sets the directories to scan for skills
func (r *SkillRegistry) SetScanDirs(dirs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanDirs = dirs
	r.scanned = false // force rescan
}

// Scan discovers all available skills
func (r *SkillRegistry) Scan() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.scanned {
		return nil
	}

	r.skills = make(map[string]SkillInfo)

	// Build list of directories to scan
	dirs := r.scanDirs
	if len(dirs) == 0 {
		dirs = r.defaultScanDirs()
	}

	for _, dir := range dirs {
		if err := r.scanDir(dir); err != nil {
			// Log but continue - don't fail on individual directory errors
			continue
		}
	}

	r.scanned = true
	return nil
}

// defaultScanDirs returns the default directories to scan for skills
func (r *SkillRegistry) defaultScanDirs() []string {
	var dirs []string

	// Current directory and parents for .claude/skills/
	cwd, _ := os.Getwd()
	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		claudeDir := filepath.Join(dir, ".claude")
		if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
			dirs = append(dirs, claudeDir)
		}
	}

	// Global ~/.claude/
	if home, err := os.UserHomeDir(); err == nil {
		globalClaude := filepath.Join(home, ".claude")
		if info, err := os.Stat(globalClaude); err == nil && info.IsDir() {
			dirs = append(dirs, globalClaude)
		}
	}

	// Current directory and parents for .opencode/
	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		opencodeDir := filepath.Join(dir, ".opencode")
		if info, err := os.Stat(opencodeDir); err == nil && info.IsDir() {
			dirs = append(dirs, opencodeDir)
		}
	}

	return dirs
}

// scanDir scans a directory for SKILL.md files
func (r *SkillRegistry) scanDir(dir string) error {
	// Pattern for .claude/skills/**/SKILL.md
	skillsDir := filepath.Join(dir, "skills")
	if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
		_ = filepath.Walk(skillsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() && info.Name() == "SKILL.md" {
				if skill, err := r.parseSkillFile(path); err == nil {
					r.skills[skill.Name] = skill
				}
			}
			return nil
		})
	}

	// Pattern for .opencode/skill/**/SKILL.md
	skillDir := filepath.Join(dir, "skill")
	if info, err := os.Stat(skillDir); err == nil && info.IsDir() {
		_ = filepath.Walk(skillDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() && info.Name() == "SKILL.md" {
				if skill, err := r.parseSkillFile(path); err == nil {
					r.skills[skill.Name] = skill
				}
			}
			return nil
		})
	}

	return nil
}

// parseSkillFile parses a SKILL.md file and extracts metadata
func (r *SkillRegistry) parseSkillFile(path string) (SkillInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return SkillInfo{}, err
	}
	defer file.Close()

	// Parse YAML frontmatter
	scanner := bufio.NewScanner(file)
	var inFrontmatter bool
	var frontmatter []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			} else {
				// End of frontmatter
				break
			}
		}
		if inFrontmatter {
			frontmatter = append(frontmatter, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return SkillInfo{}, err
	}

	// Parse frontmatter for name and description
	skill := SkillInfo{Location: path}
	for _, line := range frontmatter {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			skill.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			// Remove quotes if present
			skill.Name = strings.Trim(skill.Name, "\"'")
		} else if strings.HasPrefix(line, "description:") {
			skill.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			// Remove quotes if present
			skill.Description = strings.Trim(skill.Description, "\"'")
		}
	}

	if skill.Name == "" {
		return SkillInfo{}, fmt.Errorf("skill file missing name: %s", path)
	}

	return skill, nil
}

// Get returns a skill by name
func (r *SkillRegistry) Get(name string) (SkillInfo, bool) {
	_ = r.Scan() // Ensure scanned

	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[name]
	return skill, ok
}

// All returns all discovered skills
func (r *SkillRegistry) All() []SkillInfo {
	_ = r.Scan() // Ensure scanned

	r.mu.RLock()
	defer r.mu.RUnlock()

	skills := make([]SkillInfo, 0, len(r.skills))
	for _, s := range r.skills {
		skills = append(skills, s)
	}
	return skills
}

// LoadContent loads the full content of a skill file
func (r *SkillRegistry) LoadContent(skill SkillInfo) (string, error) {
	data, err := os.ReadFile(skill.Location)
	if err != nil {
		return "", err
	}

	content := string(data)

	// Remove frontmatter
	if strings.HasPrefix(content, "---") {
		// Find the closing ---
		rest := content[3:]
		if idx := strings.Index(rest, "\n---"); idx != -1 {
			content = strings.TrimPrefix(rest[idx+4:], "\n")
		}
	}

	return strings.TrimSpace(content), nil
}

// buildSkillDescription builds the tool description with available skills
func buildSkillDescription() string {
	skills := skillRegistry.All()

	if len(skills) == 0 {
		return "Load a skill to get detailed instructions for a specific task. No skills are currently available."
	}

	var sb strings.Builder
	sb.WriteString("Load a skill to get detailed instructions for a specific task. ")
	sb.WriteString("Skills provide specialized knowledge and step-by-step guidance. ")
	sb.WriteString("Use this when a task matches an available skill's description. ")
	sb.WriteString("Only the skills listed here are available: ")
	sb.WriteString("<available_skills>")

	// Match opencode's formatting with extra spaces
	for _, skill := range skills {
		sb.WriteString(fmt.Sprintf("   <skill>     <name>%s</name>     <description>%s</description>   </skill>",
			skill.Name, skill.Description))
	}

	sb.WriteString(" </available_skills>")
	return sb.String()
}

// buildSkillSchema builds the tool schema with dynamic examples
func buildSkillSchema() json.RawMessage {
	skills := skillRegistry.All()

	// Build examples list
	examples := make([]string, 0, len(skills))
	for _, skill := range skills {
		examples = append(examples, "'"+skill.Name+"'")
	}

	// Build description with examples
	nameDesc := "The skill identifier from available_skills"
	if len(examples) > 0 {
		nameDesc += fmt.Sprintf(" (e.g., %s, ...)", strings.Join(examples, ", "))
	}

	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]any{
			"name": map[string]any{
				"description": nameDesc,
				"type":        "string",
			},
		},
		"required":             []string{"name"},
		"additionalProperties": false,
	}

	data, _ := json.Marshal(schema)
	return data
}

// Skill creates the skill tool
func Skill() tool.Tool {
	return tool.New("skill").
		Description(buildSkillDescription()).
		Schema(buildSkillSchema()).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args SkillInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			// Look up the skill
			skill, found := skillRegistry.Get(args.Name)
			if !found {
				// List available skills in error
				all := skillRegistry.All()
				names := make([]string, len(all))
				for i, s := range all {
					names[i] = s.Name
				}
				available := "none"
				if len(names) > 0 {
					available = strings.Join(names, ", ")
				}
				return tool.Result{}, fmt.Errorf("skill %q not found. Available skills: %s", args.Name, available)
			}

			// Load skill content
			content, err := skillRegistry.LoadContent(skill)
			if err != nil {
				return tool.Result{}, fmt.Errorf("failed to load skill content: %w", err)
			}

			// Format output like opencode
			dir := filepath.Dir(skill.Location)
			output := fmt.Sprintf("## Skill: %s\n\n**Base directory**: %s\n\n%s",
				skill.Name, dir, content)

			return tool.Result{
				Output: output,
				Title:  fmt.Sprintf("Loaded skill: %s", skill.Name),
				Metadata: map[string]any{
					"name": skill.Name,
					"dir":  dir,
				},
			}, nil
		}).
		Build()
}
