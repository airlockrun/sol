// Package tools provides the tool implementations for Sol.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/airlockrun/goai/tool"
)

// ApplyPatchInput is the input schema for the apply_patch tool
type ApplyPatchInput struct {
	PatchText string `json:"patchText" description:"The full patch text that describes all changes to be made"`
}

// ApplyPatch returns the apply_patch tool definition.
// This tool is used by gpt-5 models (codex-style patching).
func ApplyPatch() tool.Tool {
	return tool.New("apply_patch").
		Description("Use the `apply_patch` tool to edit files. Your patch language is a stripped‑down, file‑oriented diff format designed to be easy to parse and safe to apply. You can think of it as a high‑level envelope:\n\n*** Begin Patch\n[ one or more file sections ]\n*** End Patch\n\nWithin that envelope, you get a sequence of file operations.\nYou MUST include a header to specify the action you are taking.\nEach operation starts with one of three headers:\n\n*** Add File: <path> - create a new file. Every following line is a + line (the initial contents).\n*** Delete File: <path> - remove an existing file. Nothing follows.\n*** Update File: <path> - patch an existing file in place (optionally with a rename).\n\nExample patch:\n\n```\n*** Begin Patch\n*** Add File: hello.txt\n+Hello world\n*** Update File: src/app.py\n*** Move to: src/main.py\n@@ def greet():\n-print(\"Hi\")\n+print(\"Hello, world!\")\n*** Delete File: obsolete.txt\n*** End Patch\n```\n\nIt is important to remember:\n\n- You must include a header with your intended action (Add/Delete/Update)\n- You must prefix new lines with `+` even when creating a new file\n").
		SchemaFromStruct(ApplyPatchInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args ApplyPatchInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			if args.PatchText == "" {
				return tool.Result{Output: "patchText is required"}, nil
			}

			workDir, _ := ctx.Value(WorkDirKey).(string)
			if workDir == "" {
				workDir, _ = os.Getwd()
			}

			// Parse the patch
			hunks, err := parsePatch(args.PatchText)
			if err != nil {
				return tool.Result{Output: fmt.Sprintf("apply_patch verification failed: %v", err)}, nil
			}

			if len(hunks) == 0 {
				normalized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(args.PatchText, "\r\n", "\n"), "\r", "\n"))
				if normalized == "*** Begin Patch\n*** End Patch" {
					return tool.Result{Output: "patch rejected: empty patch"}, nil
				}
				return tool.Result{Output: "apply_patch verification failed: no hunks found"}, nil
			}

			// Helper to get relative path for summary
			// Use "/" as base to match opencode behavior (Instance.worktree is typically root)
			relPath := func(p string) string {
				if strings.HasPrefix(p, "/") {
					return p[1:] // Strip leading slash to make relative
				}
				return p
			}

			// Apply the hunks
			var summaryLines []string
			for _, hunk := range hunks {
				filePath := hunk.Path
				if !filepath.IsAbs(filePath) {
					filePath = filepath.Join(workDir, hunk.Path)
				}

				switch hunk.Type {
				case "add":
					// Create parent directories
					if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
						return tool.Result{Output: fmt.Sprintf("failed to create directory: %v", err)}, nil
					}
					content := hunk.Contents
					if !strings.HasSuffix(content, "\n") && content != "" {
						content += "\n"
					}
					if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
						return tool.Result{Output: fmt.Sprintf("failed to write file: %v", err)}, nil
					}
					summaryLines = append(summaryLines, fmt.Sprintf("A %s", relPath(filePath)))

				case "delete":
					if err := os.Remove(filePath); err != nil {
						return tool.Result{Output: fmt.Sprintf("failed to delete file: %v", err)}, nil
					}
					summaryLines = append(summaryLines, fmt.Sprintf("D %s", relPath(filePath)))

				case "update":
					oldContent, err := os.ReadFile(filePath)
					if err != nil {
						return tool.Result{Output: fmt.Sprintf("apply_patch verification failed: Failed to read file to update: %s", filePath)}, nil
					}

					newContent, err := applyUpdateChunks(string(oldContent), hunk.Chunks)
					if err != nil {
						return tool.Result{Output: fmt.Sprintf("apply_patch verification failed: %v", err)}, nil
					}

					targetPath := filePath
					if hunk.MovePath != "" {
						targetPath = hunk.MovePath
						if !filepath.IsAbs(targetPath) {
							targetPath = filepath.Join(workDir, hunk.MovePath)
						}
						// Create parent directories for move target
						if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
							return tool.Result{Output: fmt.Sprintf("failed to create directory: %v", err)}, nil
						}
					}

					if err := os.WriteFile(targetPath, []byte(newContent), 0644); err != nil {
						return tool.Result{Output: fmt.Sprintf("failed to write file: %v", err)}, nil
					}

					// Delete original file if moved
					if hunk.MovePath != "" && filePath != targetPath {
						os.Remove(filePath)
						summaryLines = append(summaryLines, fmt.Sprintf("M %s -> %s", relPath(filePath), relPath(targetPath)))
					} else {
						summaryLines = append(summaryLines, fmt.Sprintf("M %s", relPath(targetPath)))
					}
				}
			}

			output := fmt.Sprintf("Success. Updated the following files:\n%s", strings.Join(summaryLines, "\n"))
			return tool.Result{Output: output, Title: output}, nil
		}).
		Build()
}

// Hunk represents a single file operation in a patch
type Hunk struct {
	Type     string // "add", "delete", "update"
	Path     string
	MovePath string        // for renames
	Contents string        // for add
	Chunks   []UpdateChunk // for update
}

// UpdateChunk represents a change within an update operation
type UpdateChunk struct {
	Context  string
	OldLines []string
	NewLines []string
	IsEOF    bool
}

func parsePatch(patchText string) ([]Hunk, error) {
	// Strip heredoc wrapper if present
	cleaned := stripHeredoc(strings.TrimSpace(patchText))
	lines := strings.Split(cleaned, "\n")

	// Find Begin/End markers
	beginIdx := -1
	endIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "*** Begin Patch" {
			beginIdx = i
		} else if trimmed == "*** End Patch" {
			endIdx = i
		}
	}

	if beginIdx == -1 || endIdx == -1 || beginIdx >= endIdx {
		return nil, fmt.Errorf("invalid patch format: missing Begin/End markers")
	}

	var hunks []Hunk
	i := beginIdx + 1

	for i < endIdx {
		line := lines[i]

		if strings.HasPrefix(line, "*** Add File:") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
			i++

			// Collect content lines
			var contentLines []string
			for i < endIdx && !strings.HasPrefix(lines[i], "***") {
				if strings.HasPrefix(lines[i], "+") {
					contentLines = append(contentLines, strings.TrimPrefix(lines[i], "+"))
				}
				i++
			}

			hunks = append(hunks, Hunk{
				Type:     "add",
				Path:     path,
				Contents: strings.Join(contentLines, "\n"),
			})

		} else if strings.HasPrefix(line, "*** Delete File:") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
			hunks = append(hunks, Hunk{
				Type: "delete",
				Path: path,
			})
			i++

		} else if strings.HasPrefix(line, "*** Update File:") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			i++

			var movePath string
			if i < endIdx && strings.HasPrefix(lines[i], "*** Move to:") {
				movePath = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to:"))
				i++
			}

			// Parse chunks
			var chunks []UpdateChunk
			for i < endIdx && !strings.HasPrefix(lines[i], "*** ") {
				if strings.HasPrefix(lines[i], "@@") {
					context := strings.TrimSpace(strings.TrimPrefix(lines[i], "@@"))
					i++

					var oldLines, newLines []string
					isEOF := false

					for i < endIdx && !strings.HasPrefix(lines[i], "@@") && !strings.HasPrefix(lines[i], "*** ") {
						changeLine := lines[i]

						if changeLine == "*** End of File" {
							isEOF = true
							i++
							break
						}

						if strings.HasPrefix(changeLine, " ") {
							content := strings.TrimPrefix(changeLine, " ")
							oldLines = append(oldLines, content)
							newLines = append(newLines, content)
						} else if strings.HasPrefix(changeLine, "-") {
							oldLines = append(oldLines, strings.TrimPrefix(changeLine, "-"))
						} else if strings.HasPrefix(changeLine, "+") {
							newLines = append(newLines, strings.TrimPrefix(changeLine, "+"))
						}
						i++
					}

					chunks = append(chunks, UpdateChunk{
						Context:  context,
						OldLines: oldLines,
						NewLines: newLines,
						IsEOF:    isEOF,
					})
				} else {
					i++
				}
			}

			hunks = append(hunks, Hunk{
				Type:     "update",
				Path:     path,
				MovePath: movePath,
				Chunks:   chunks,
			})

		} else {
			i++
		}
	}

	return hunks, nil
}

func stripHeredoc(input string) string {
	// Match heredoc patterns like: cat <<'EOF'\n...\nEOF
	lines := strings.Split(input, "\n")
	if len(lines) < 3 {
		return input
	}

	// Check for heredoc start
	firstLine := lines[0]
	if strings.Contains(firstLine, "<<") {
		// Extract delimiter
		parts := strings.Split(firstLine, "<<")
		if len(parts) >= 2 {
			delim := strings.Trim(parts[1], "'\"")
			delim = strings.TrimSpace(delim)

			// Check if last line is the delimiter
			lastLine := strings.TrimSpace(lines[len(lines)-1])
			if lastLine == delim {
				// Return content between
				return strings.Join(lines[1:len(lines)-1], "\n")
			}
		}
	}

	return input
}

func applyUpdateChunks(originalContent string, chunks []UpdateChunk) (string, error) {
	lines := strings.Split(originalContent, "\n")
	// Remove trailing empty line if present (for consistent counting)
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Compute and apply replacements
	replacements, err := computeReplacements(lines, chunks)
	if err != nil {
		return "", err
	}

	result := applyReplacements(lines, replacements)

	// Ensure trailing newline
	if len(result) == 0 || result[len(result)-1] != "" {
		result = append(result, "")
	}

	return strings.Join(result, "\n"), nil
}

type replacement struct {
	startIdx int
	oldLen   int
	newLines []string
}

func computeReplacements(lines []string, chunks []UpdateChunk) ([]replacement, error) {
	var replacements []replacement
	lineIndex := 0

	for _, chunk := range chunks {
		// Handle context-based seeking
		if chunk.Context != "" {
			idx := seekSequence(lines, []string{chunk.Context}, lineIndex, false)
			if idx == -1 {
				return nil, fmt.Errorf("failed to find context '%s'", chunk.Context)
			}
			lineIndex = idx + 1
		}

		// Handle pure addition (no old lines)
		if len(chunk.OldLines) == 0 {
			insertIdx := len(lines)
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				insertIdx = len(lines) - 1
			}
			replacements = append(replacements, replacement{
				startIdx: insertIdx,
				oldLen:   0,
				newLines: chunk.NewLines,
			})
			continue
		}

		// Try to match old lines
		pattern := chunk.OldLines
		newSlice := chunk.NewLines
		found := seekSequence(lines, pattern, lineIndex, chunk.IsEOF)

		// Retry without trailing empty line
		if found == -1 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(newSlice) > 0 && newSlice[len(newSlice)-1] == "" {
				newSlice = newSlice[:len(newSlice)-1]
			}
			found = seekSequence(lines, pattern, lineIndex, chunk.IsEOF)
		}

		if found == -1 {
			return nil, fmt.Errorf("failed to find expected lines:\n%s", strings.Join(chunk.OldLines, "\n"))
		}

		replacements = append(replacements, replacement{
			startIdx: found,
			oldLen:   len(pattern),
			newLines: newSlice,
		})
		lineIndex = found + len(pattern)
	}

	return replacements, nil
}

func applyReplacements(lines []string, replacements []replacement) []string {
	result := make([]string, len(lines))
	copy(result, lines)

	// Apply in reverse order to avoid index shifting
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		// Remove old lines
		result = append(result[:r.startIdx], result[r.startIdx+r.oldLen:]...)
		// Insert new lines
		newResult := make([]string, 0, len(result)+len(r.newLines))
		newResult = append(newResult, result[:r.startIdx]...)
		newResult = append(newResult, r.newLines...)
		newResult = append(newResult, result[r.startIdx:]...)
		result = newResult
	}

	return result
}

func seekSequence(lines []string, pattern []string, startIndex int, eof bool) int {
	if len(pattern) == 0 {
		return -1
	}

	// Helper for matching
	tryMatch := func(compare func(a, b string) bool) int {
		// If EOF anchor, try from end first
		if eof {
			fromEnd := len(lines) - len(pattern)
			if fromEnd >= startIndex {
				matches := true
				for j := 0; j < len(pattern); j++ {
					if !compare(lines[fromEnd+j], pattern[j]) {
						matches = false
						break
					}
				}
				if matches {
					return fromEnd
				}
			}
		}

		// Forward search
		for i := startIndex; i <= len(lines)-len(pattern); i++ {
			matches := true
			for j := 0; j < len(pattern); j++ {
				if !compare(lines[i+j], pattern[j]) {
					matches = false
					break
				}
			}
			if matches {
				return i
			}
		}
		return -1
	}

	// Pass 1: exact match
	if idx := tryMatch(func(a, b string) bool { return a == b }); idx != -1 {
		return idx
	}

	// Pass 2: rstrip (trim trailing whitespace)
	if idx := tryMatch(func(a, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") }); idx != -1 {
		return idx
	}

	// Pass 3: trim (both ends)
	if idx := tryMatch(func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }); idx != -1 {
		return idx
	}

	// Pass 4: normalized (Unicode punctuation to ASCII)
	if idx := tryMatch(func(a, b string) bool {
		return normalizeUnicode(strings.TrimSpace(a)) == normalizeUnicode(strings.TrimSpace(b))
	}); idx != -1 {
		return idx
	}

	return -1
}

func normalizeUnicode(s string) string {
	// Normalize Unicode punctuation to ASCII
	replacer := strings.NewReplacer(
		"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'", // single quotes
		"\u201C", "\"", "\u201D", "\"", "\u201E", "\"", "\u201F", "\"", // double quotes
		"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-", // dashes
		"\u2026", "...", // ellipsis
		"\u00A0", " ", // non-breaking space
	)
	return replacer.Replace(s)
}
