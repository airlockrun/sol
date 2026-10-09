package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/toolutil"
	_ "golang.org/x/image/webp"
)

const (
	defaultReadLimit  = 2000
	maxLineLength     = 2000
	maxReadBytes      = 50 * 1024       // 50KB
	maxReadImageBytes = 5 * 1024 * 1024 // 5 MiB of image file bytes
)

var errReadNonRegular = errors.New("cannot read non-regular file")

// ReadInput is the input schema for the read tool
type ReadInput struct {
	FilePath string `json:"filePath" description:"The path to the file to read"`
	Offset   int    `json:"offset,omitempty" description:"Text files only: the line number to start reading from (0-based); omit for images"`
	Limit    int    `json:"limit,omitempty" description:"Text files only: the number of lines to read (defaults to 2000); omit for images"`
}

// Read creates the read tool
func Read() tool.Tool {
	return tool.New("read").
		Description(`Reads a file from the local filesystem. You can access any file directly by using this tool.
Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid. It is okay to read a file that does not exist; an error will be returned.

Usage:
- The filePath parameter must be an absolute path, not a relative path
- Only regular files can be read. Symbolic links to regular files are supported; pipes and device files are rejected before reading.
- By default, it reads up to 2000 lines starting from the beginning of the file
- You can optionally specify a line offset and limit (especially handy for long files), but it's recommended to read the whole file by not providing these parameters
- Any lines longer than 2000 characters will be truncated
- Results are returned using cat -n format, with line numbers starting at 1
- PNG, JPEG, GIF, and WebP image files are returned as native image attachments for visual inspection, not OCR text. Formats are identified from file content, not the extension.
- Images are read whole, with a maximum file size of 5 MiB (5242880 bytes). Omit offset and limit for images; supplying either is an error. Resize an oversized image before reading it.
- You have the capability to call multiple tools in a single response. It is always better to speculatively read multiple files as a batch that are potentially useful.
`).
		SchemaFromStruct(ReadInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			if err := ctx.Err(); err != nil {
				return tool.Result{}, err
			}
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

			info, err := os.Stat(filePath)
			if err != nil || info.IsDir() {
				return tool.Result{Output: fmt.Sprintf("Error: File not found: %s", filePath), Title: filepath.Base(filePath)}, nil
			}
			if !info.Mode().IsRegular() {
				return tool.Result{}, fmt.Errorf("%w: %s", errReadNonRegular, filePath)
			}
			file, err := openReadFile(filePath)
			if err != nil {
				if errors.Is(err, errReadNonRegular) {
					return tool.Result{}, err
				}
				return tool.Result{Output: fmt.Sprintf("Error: File not found: %s", filePath), Title: filepath.Base(filePath)}, nil
			}
			defer file.Close()
			if err := ctx.Err(); err != nil {
				return tool.Result{}, err
			}
			// A bounded content sniff precedes binary rejection, including when an
			// image has a misleading extension. Text retains its ordinary limits.
			header := make([]byte, 512)
			n, err := io.ReadFull(file, header)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return tool.Result{}, fmt.Errorf("read file header: %w", err)
			}
			header = header[:n]
			contentReader := io.MultiReader(bytes.NewReader(header), file)
			mimeType := http.DetectContentType(header)
			switch mimeType {
			case "image/png", "image/jpeg", "image/gif", "image/webp":
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(input, &fields); err != nil {
					return tool.Result{}, err
				}
				_, offsetSet := fields["offset"]
				_, limitSet := fields["limit"]
				if offsetSet || limitSet {
					return tool.Result{}, errors.New("offset and limit apply only to text files; omit both when reading an image")
				}
				result, err := readImage(ctx, file, contentReader, filePath, mimeType)
				if err == nil && sessionID != "" {
					toolutil.FileTime.Read(sessionID, filePath)
				}
				return result, err
			}

			// Reject other known binary formats without loading the whole file.
			if isBinaryByExtension(filePath) {
				return tool.Result{
					Output: fmt.Sprintf("Cannot read binary file: %s", filePath),
					Title:  filepath.Base(filePath),
				}, nil
			}

			content, err := io.ReadAll(contentReader)
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
			// A trailing newline (most text files end with one) makes Split
			// emit a spurious empty final element. Drop it so totalLines and
			// the footer reflect the real line count instead of an off-by-one
			// with a phantom blank last line.
			if n := len(lines); n > 1 && lines[n-1] == "" && strings.HasSuffix(string(content), "\n") {
				lines = lines[:n-1]
			}
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
				// Truncate on a rune boundary — the doc promises "characters",
				// and slicing raw bytes at maxLineLength could split a multibyte
				// rune and emit invalid UTF-8. Only pay the []rune cost for lines
				// long enough to possibly exceed the limit.
				if len(line) > maxLineLength {
					if r := []rune(line); len(r) > maxLineLength {
						line = string(r[:maxLineLength]) + "..."
					}
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

			// Footer mirrors opencode: surface the visible line range and the
			// exact offset to continue from, so the model paginates a truncated
			// file instead of proceeding on a partial read. offset is 0-based,
			// so the next chunk starts at offset+len(raw) — the line right after
			// the last one shown.
			lastReadLine := offset + len(raw)
			firstLine := offset + 1
			hasMoreLines := totalLines > lastReadLine

			switch {
			case truncatedByBytes:
				result.WriteString(fmt.Sprintf("\n(Output capped at %d KB. Showing lines %d-%d. Use offset=%d to continue.)",
					maxReadBytes/1024, firstLine, lastReadLine, lastReadLine))
			case hasMoreLines:
				result.WriteString(fmt.Sprintf("\n(Showing lines %d-%d of %d. Use offset=%d to continue.)",
					firstLine, lastReadLine, totalLines, lastReadLine))
			default:
				result.WriteString(fmt.Sprintf("\n(End of file - total %d lines)", totalLines))
			}
			result.WriteString("\n</file>")

			return tool.Result{
				Output: result.String(),
				Title:  filepath.Base(filePath),
				Metadata: map[string]any{
					"lines":      len(raw),
					"bytes":      bytes,
					"totalLines": totalLines,
				},
			}, nil
		}).
		Build()
}

// openReadFile verifies the descriptor independently of the path check. Unix
// opens are nonblocking so a replacement FIFO cannot block before this check.
func openReadFile(filePath string) (*os.File, error) {
	file, err := openReadDescriptor(filePath)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%w: %s", errReadNonRegular, filePath)
	}
	return file, nil
}

func readImage(ctx context.Context, file *os.File, reader io.Reader, filePath, mimeType string) (tool.Result, error) {
	info, err := file.Stat()
	if err != nil {
		return tool.Result{}, fmt.Errorf("stat image: %w", err)
	}
	if !info.Mode().IsRegular() {
		return tool.Result{}, errors.New("image must be a regular file")
	}
	if info.Size() > maxReadImageBytes {
		return tool.Result{}, fmt.Errorf("image exceeds the read limit of 5 MiB (%d bytes); resize it before reading", maxReadImageBytes)
	}
	// LimitReader also bounds a file that grows after Stat.
	content, err := io.ReadAll(io.LimitReader(reader, maxReadImageBytes+1))
	if err != nil {
		return tool.Result{}, fmt.Errorf("read image: %w", err)
	}
	if len(content) > maxReadImageBytes {
		return tool.Result{}, fmt.Errorf("image exceeds the read limit of 5 MiB (%d bytes); resize it before reading", maxReadImageBytes)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	// DecodeConfig validates the format header and dimensions without allocating
	// a decompressed pixel buffer. The original encoded bytes reach the model.
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return tool.Result{}, fmt.Errorf("invalid %s image: %w", mimeType, err)
	}
	formats := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}
	if formats[format] != mimeType || config.Width <= 0 || config.Height <= 0 {
		return tool.Result{}, errors.New("invalid image format or dimensions")
	}
	return tool.Result{
		Output:      fmt.Sprintf("Image attached: %s (%s, %dx%d, %d bytes).", filePath, mimeType, config.Width, config.Height, len(content)),
		Title:       filepath.Base(filePath),
		Attachments: []tool.Attachment{{Data: base64.StdEncoding.EncodeToString(content), MimeType: mimeType, Filename: filepath.Base(filePath)}},
		Metadata:    map[string]any{"bytes": len(content), "mimeType": mimeType, "width": config.Width, "height": config.Height},
	}, nil
}

// isBinaryByExtension checks if a file is binary based on its extension.
// Uses filepath.Ext so an extensionless path (Dockerfile, Makefile, LICENSE)
// yields "" rather than panicking — LastIndex returns -1 for a path with no
// dot, and the old path[-1:] slice blew up on exactly those files.
func isBinaryByExtension(p string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
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
