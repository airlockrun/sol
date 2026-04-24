package toolutil

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// TruncateMaxLines is the maximum number of lines before truncation
	TruncateMaxLines = 2000
	// TruncateMaxBytes is the maximum bytes before truncation (50KB)
	TruncateMaxBytes = 50 * 1024
)

// TruncateResult contains the result of truncation
type TruncateResult struct {
	Content    string
	Truncated  bool
	OutputPath string
}

// TruncateOptions configures truncation behavior
type TruncateOptions struct {
	MaxLines  int
	MaxBytes  int
	Direction string // "head" or "tail"
}

// TruncateOutput truncates output that exceeds limits and saves full output to file
func TruncateOutput(text string, opts TruncateOptions) TruncateResult {
	if opts.MaxLines <= 0 {
		opts.MaxLines = TruncateMaxLines
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = TruncateMaxBytes
	}
	if opts.Direction == "" {
		opts.Direction = "head"
	}

	lines := splitLines(text)
	totalBytes := len(text)

	if len(lines) <= opts.MaxLines && totalBytes <= opts.MaxBytes {
		return TruncateResult{Content: text, Truncated: false}
	}

	var out []string
	bytes := 0
	hitBytes := false

	if opts.Direction == "head" {
		for i := 0; i < len(lines) && i < opts.MaxLines; i++ {
			size := len(lines[i])
			if i > 0 {
				size++ // newline
			}
			if bytes+size > opts.MaxBytes {
				hitBytes = true
				break
			}
			out = append(out, lines[i])
			bytes += size
		}
	} else {
		// tail direction
		for i := len(lines) - 1; i >= 0 && len(out) < opts.MaxLines; i-- {
			size := len(lines[i])
			if len(out) > 0 {
				size++ // newline
			}
			if bytes+size > opts.MaxBytes {
				hitBytes = true
				break
			}
			out = append([]string{lines[i]}, out...)
			bytes += size
		}
	}

	removed := len(lines) - len(out)
	unit := "lines"
	if hitBytes {
		removed = totalBytes - bytes
		unit = "bytes"
	}

	preview := joinLines(out)

	// Save full output to temp file
	outputPath := saveTruncatedOutput(text)

	hint := fmt.Sprintf("The tool call succeeded but the output was truncated. Full output saved to: %s\nUse Grep to search the full content or Read with offset/limit to view specific sections.", outputPath)

	var message string
	if opts.Direction == "head" {
		message = fmt.Sprintf("%s\n\n...%d %s truncated...\n\n%s", preview, removed, unit, hint)
	} else {
		message = fmt.Sprintf("...%d %s truncated...\n\n%s\n\n%s", removed, unit, hint, preview)
	}

	return TruncateResult{
		Content:    message,
		Truncated:  true,
		OutputPath: outputPath,
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	result := lines[0]
	for i := 1; i < len(lines); i++ {
		result += "\n" + lines[i]
	}
	return result
}

func saveTruncatedOutput(content string) string {
	// Create output directory
	dir := filepath.Join(os.TempDir(), "sol-tool-output")
	os.MkdirAll(dir, 0755)

	// Generate unique filename
	filename := fmt.Sprintf("tool_%d.txt", time.Now().UnixNano())
	outputPath := filepath.Join(dir, filename)

	// Write file
	os.WriteFile(outputPath, []byte(content), 0644)

	return outputPath
}
