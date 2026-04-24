package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/airlockrun/goai/tool"
)

const globLimit = 100

// GlobInput is the input schema for the glob tool
type GlobInput struct {
	Pattern string `json:"pattern" description:"The glob pattern to match files against"`
	Path    string `json:"path,omitempty" description:"The directory to search in. If not specified, the current working directory will be used. IMPORTANT: Omit this field to use the default directory. DO NOT enter \"undefined\" or \"null\" - simply omit it for the default behavior. Must be a valid directory path if provided."`
}

// Glob creates the glob tool
func Glob() tool.Tool {
	return tool.New("glob").
		Description(`- Fast file pattern matching tool that works with any codebase size
- Supports glob patterns like "**/*.js" or "src/**/*.ts"
- Returns matching file paths sorted by modification time
- Use this tool when you need to find files by name patterns
- When you are doing an open-ended search that may require multiple rounds of globbing and grepping, use the Task tool instead
- You have the capability to call multiple tools in a single response. It is always better to speculatively perform multiple searches as a batch that are potentially useful.
`).
		SchemaFromStruct(GlobInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args GlobInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			// Get working directory from context
			workDir, _ := ctx.Value(WorkDirKey).(string)

			searchPath := args.Path
			if searchPath == "" {
				if workDir != "" {
					searchPath = workDir
				} else {
					searchPath = "."
				}
			} else if !filepath.IsAbs(searchPath) && workDir != "" {
				// Make relative paths relative to workDir
				searchPath = filepath.Join(workDir, searchPath)
			}
			searchPath, _ = filepath.Abs(searchPath)

			// Try ripgrep first (preferred)
			files, truncated := globWithRipgrep(ctx, searchPath, args.Pattern)

			// Fallback to find if ripgrep not available
			if files == nil {
				files, truncated = globWithFind(ctx, searchPath, args.Pattern)
			}

			// Format output (matching opencode)
			var output []string
			if len(files) == 0 {
				output = append(output, "No files found")
			} else {
				output = append(output, files...)
				if truncated {
					output = append(output, "")
					output = append(output, "(Results are truncated. Consider using a more specific path or pattern.)")
				}
			}

			return tool.Result{
				Output: strings.Join(output, "\n"),
				Title:  filepath.Base(searchPath),
			}, nil
		}).
		Build()
}

type fileWithMtime struct {
	path  string
	mtime int64
}

func globWithRipgrep(ctx context.Context, searchPath, pattern string) ([]string, bool) {
	// Use ripgrep's --files with glob pattern
	// Run from searchPath directory (matching opencode behavior)
	cmd := exec.CommandContext(ctx, "rg", "--files", "--follow", "--hidden", "--glob=!.git/*", "--glob", pattern)
	cmd.Dir = searchPath
	output, err := cmd.Output()
	if err != nil {
		return nil, false // ripgrep not available or error
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, false
	}

	// Get mtime for each file and convert to absolute paths (matching opencode behavior)
	files := make([]fileWithMtime, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		fullPath := line
		if !filepath.IsAbs(line) {
			fullPath = filepath.Join(searchPath, line)
		}
		var mtime int64
		if info, err := os.Stat(fullPath); err == nil {
			mtime = info.ModTime().UnixNano()
		}
		files = append(files, fileWithMtime{path: fullPath, mtime: mtime})
	}

	// Sort by mtime descending (most recent first), matching opencode behavior
	// When mtimes are equal, sort alphabetically by path for deterministic results
	// (ripgrep order is filesystem-dependent and not deterministic)
	sort.Slice(files, func(i, j int) bool {
		if files[i].mtime != files[j].mtime {
			return files[i].mtime > files[j].mtime
		}
		return files[i].path < files[j].path
	})

	truncated := len(files) > globLimit
	if truncated {
		files = files[:globLimit]
	}

	result := make([]string, len(files))
	for i, f := range files {
		result[i] = f.path
	}

	return result, truncated
}

func globWithFind(ctx context.Context, searchPath, pattern string) ([]string, bool) {
	// find's -name only matches a single filename component, so strip any
	// leading directory glob prefixes like "**/" or "src/**/".
	namePattern := pattern
	if idx := strings.LastIndex(pattern, "/"); idx >= 0 {
		namePattern = pattern[idx+1:]
	}
	// If nothing useful remains after stripping (e.g., pattern was "**"),
	// match all files.
	if namePattern == "" || namePattern == "**" {
		namePattern = "*"
	}

	cmd := exec.CommandContext(ctx, "find", searchPath, "-name", namePattern, "-type", "f", "-not", "-path", "*/.git/*")
	output, _ := cmd.Output()

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, false
	}

	// Sort by modification time descending (consistent with ripgrep path).
	files := make([]fileWithMtime, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var mtime int64
		if info, err := os.Stat(line); err == nil {
			mtime = info.ModTime().UnixNano()
		}
		files = append(files, fileWithMtime{path: line, mtime: mtime})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mtime != files[j].mtime {
			return files[i].mtime > files[j].mtime
		}
		return files[i].path < files[j].path
	})

	truncated := len(files) > globLimit
	if truncated {
		files = files[:globLimit]
	}

	result := make([]string, len(files))
	for i, f := range files {
		result[i] = f.path
	}
	return result, truncated
}
