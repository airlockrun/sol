package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/toolutil"
)

const (
	defaultReadLimit = 2000
	maxLineLength    = 2000
	maxReadBytes     = 50 * 1024 // 50KB
)

// ReadInput is the input schema for the read tool
type ReadInput struct {
	FilePath string `json:"filePath" description:"The path to the file to read"`
	Offset   int    `json:"offset,omitempty" description:"The line number to start reading from (0-based)"`
	Limit    int    `json:"limit,omitempty" description:"The number of lines to read (defaults to 2000)"`
}

// Read creates the read tool
func Read() tool.Tool {
	return tool.New("read").
		Description(`Reads a file from the local filesystem. You can access any file directly by using this tool.
Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The filePath parameter must be an absolute path, not a relative path
- By default, it reads up to 2000 lines starting from the beginning of the file
- You can optionally specify a line offset and limit (especially handy for long files), but it's recommended to read the whole file by not providing these parameters
- Any lines longer than 2000 characters will be truncated
- Results are returned using cat -n format, with line numbers starting at 1
- You have the capability to call multiple tools in a single response. It is always better to speculatively read multiple files as a batch that are potentially useful.
- If you read a file that exists but has empty contents you will receive a system reminder warning in place of file contents.
- You can read image files using this tool.
`).
		SchemaFromStruct(ReadInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args ReadInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			// Get session ID and workdir from context
			sessionID, _ := ctx.Value(SessionIDKey).(string)
			workDir, _ := ctx.Value(WorkDirKey).(string)

			// Resolve relative paths using workdir
			filePath := args.FilePath
			if !filepath.IsAbs(filePath) && workDir != "" {
				filePath = filepath.Join(workDir, filePath)
			}

			// Check for binary file by extension
			if isBinaryByExtension(filePath) {
				return tool.Result{
					Output: fmt.Sprintf("Cannot read binary file: %s", filePath),
					Title:  filepath.Base(filePath),
				}, nil
			}

			content, err := os.ReadFile(filePath)
			if err != nil {
				return tool.Result{
					Output: fmt.Sprintf("Error: File not found: %s", filePath),
					Title:  filepath.Base(filePath),
				}, nil
			}

			// Strip UTF-8 BOM if present (matching opencode behavior)
			if len(content) >= 3 && content[0] == 0xEF && content[1] == 0xBB && content[2] == 0xBF {
				content = content[3:]
			}

			// Track when this file was read (for write/edit safety checks)
			if sessionID != "" {
				toolutil.FileTime.Read(sessionID, filePath)
			}

			// Check if file appears to be binary (null bytes or >30% non-printable)
			if isBinaryContent(content) {
				return tool.Result{
					Output: fmt.Sprintf("Cannot read binary file: %s", filePath),
					Title:  filepath.Base(filePath),
				}, nil
			}

			lines := strings.Split(string(content), "\n")
			totalLines := len(lines)

			offset := args.Offset
			if offset < 0 {
				offset = 0
			}
			limit := args.Limit
			if limit <= 0 {
				limit = defaultReadLimit
			}

			// Collect lines with byte limit check (matching opencode)
			var raw []string
			var bytes int
			truncatedByBytes := false

			for i := offset; i < len(lines) && i < offset+limit; i++ {
				line := lines[i]
				if len(line) > maxLineLength {
					line = line[:maxLineLength] + "..."
				}
				size := len(line)
				if len(raw) > 0 {
					size++ // newline
				}
				if bytes+size > maxReadBytes {
					truncatedByBytes = true
					break
				}
				raw = append(raw, line)
				bytes += size
			}

			// Format with line numbers (matching opencode: 5-digit padded + "| ")
			var result strings.Builder
			result.WriteString("<file>\n")
			for i, line := range raw {
				lineNum := i + offset + 1
				result.WriteString(fmt.Sprintf("%05d| %s\n", lineNum, line))
			}

			// Add footer message (matching opencode format)
			lastReadLine := offset + len(raw)
			hasMoreLines := totalLines > lastReadLine

			if truncatedByBytes {
				result.WriteString(fmt.Sprintf("\n(Output truncated at %d bytes. Use 'offset' parameter to read beyond line %d)", maxReadBytes, lastReadLine))
			} else if hasMoreLines {
				result.WriteString(fmt.Sprintf("\n(File has more lines. Use 'offset' parameter to read beyond line %d)", lastReadLine))
			} else {
				result.WriteString(fmt.Sprintf("\n(End of file - total %d lines)", totalLines))
			}
			result.WriteString("\n</file>")

			return tool.Result{
				Output: result.String(),
				Title:  filepath.Base(filePath),
			}, nil
		}).
		Build()
}

// isBinaryByExtension checks if a file is binary based on its extension
func isBinaryByExtension(filepath string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath[strings.LastIndex(filepath, "."):], "."))
	switch ext {
	case "zip", "tar", "gz", "exe", "dll", "so", "class", "jar", "war",
		"7z", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "ods", "odp",
		"bin", "dat", "obj", "o", "a", "lib", "wasm", "pyc", "pyo":
		return true
	}
	return false
}

// isBinaryContent checks if content appears to be binary
func isBinaryContent(content []byte) bool {
	if len(content) == 0 {
		return false
	}

	checkLen := 4096
	if len(content) < checkLen {
		checkLen = len(content)
	}

	nonPrintableCount := 0
	for i := 0; i < checkLen; i++ {
		b := content[i]
		if b == 0 {
			return true // null byte = definitely binary
		}
		if b < 9 || (b > 13 && b < 32) {
			nonPrintableCount++
		}
	}

	// If >30% non-printable characters, consider it binary
	return float64(nonPrintableCount)/float64(checkLen) > 0.3
}
