package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/toolutil"
)

const (
	grepLimit         = 100
	grepMaxLineLength = 2000
)

// GrepInput is the input schema for the grep tool
type GrepInput struct {
	Pattern string `json:"pattern" description:"The regex pattern to search for in file contents"`
	Path    string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
	Include string `json:"include,omitempty" description:"File pattern to include in the search (e.g. \"*.js\", \"*.{ts,tsx}\")"`
}

// Grep creates the grep tool
func Grep() tool.Tool {
	return tool.New("grep").
		Description(`- Fast content search tool that works with any codebase size
- Searches file contents using regular expressions
- Supports full regex syntax (eg. "log.*Error", "function\s+\w+", etc.)
- Filter files by pattern with the include parameter (eg. "*.js", "*.{ts,tsx}")
- Returns file paths and line numbers with at least one match sorted by modification time
- Use this tool when you need to find files containing specific patterns
- If you need to identify/count the number of matches within files, use the Bash tool with ` + "`rg`" + ` (ripgrep) directly. Do NOT use ` + "`grep`" + `.
- When you are doing an open-ended search that may require multiple rounds of globbing and grepping, use the Task tool instead
`).
		SchemaFromStruct(GrepInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args GrepInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			if args.Pattern == "" {
				return tool.Result{
					Output: "pattern is required",
					Title:  args.Pattern,
				}, nil
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

			// Try ripgrep first (preferred, matching opencode)
			output := grepWithRipgrep(ctx, searchPath, args.Pattern, args.Include)

			// Fallback to grep if ripgrep not available
			if output == "" {
				output = grepWithGrep(ctx, searchPath, args.Pattern, args.Include)
			}

			return tool.Result{
				Output: output,
				Title:  args.Pattern,
			}, nil
		}).
		Build()
}

func grepWithRipgrep(ctx context.Context, searchPath, pattern, include string) string {
	args := []string{
		"-nH",
		"--hidden",
		"--follow",
		"--no-messages",
		"--field-match-separator=|",
		"--regexp", pattern,
	}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, searchPath)

	cmd := exec.CommandContext(ctx, "rg", args...)
	output, err := cmd.Output()

	// Exit codes: 0 = matches found, 1 = no matches, 2 = errors
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok && exitErr.ExitCode() == 1 {
			return "No files found"
		}
		if len(output) == 0 {
			return "" // ripgrep not available or failed
		}
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return "No files found"
	}

	// Parse and format output (matching opencode format)
	type match struct {
		path    string
		lineNum string
		text    string
	}
	var matches []match

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		matches = append(matches, match{
			path:    parts[0],
			lineNum: parts[1],
			text:    parts[2],
		})
	}

	truncated := len(matches) > grepLimit
	if truncated {
		matches = matches[:grepLimit]
	}

	if len(matches) == 0 {
		return "No files found"
	}

	// Format output (matching opencode)
	var result []string
	result = append(result, fmt.Sprintf("Found %d matches", len(matches)))

	currentFile := ""
	for _, m := range matches {
		if currentFile != m.path {
			if currentFile != "" {
				result = append(result, "")
			}
			currentFile = m.path
			result = append(result, m.path+":")
		}
		text := m.text
		if len(text) > grepMaxLineLength {
			text = text[:grepMaxLineLength] + "..."
		}
		result = append(result, fmt.Sprintf("  Line %s: %s", m.lineNum, text))
	}

	if truncated {
		result = append(result, "")
		result = append(result, "(Results are truncated. Consider using a more specific path or pattern.)")
	}

	return strings.Join(result, "\n")
}

func grepWithGrep(ctx context.Context, searchPath, pattern, include string) string {
	cmdArgs := []string{"-rn", pattern, searchPath}
	if include != "" {
		cmdArgs = append([]string{"--include=" + include}, cmdArgs...)
	}

	cmd := exec.CommandContext(ctx, "grep", cmdArgs...)
	output, _ := cmd.Output()

	result := strings.TrimSpace(string(output))
	if result == "" {
		return "No files found"
	}

	// Apply truncation
	truncResult := toolutil.TruncateOutput(result, toolutil.TruncateOptions{
		MaxLines: grepLimit,
		MaxBytes: toolutil.TruncateMaxBytes,
	})

	return truncResult.Content
}
