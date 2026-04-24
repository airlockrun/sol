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

func executeReadTool(t *testing.T, input ReadInput) string {
	t.Helper()

	readTool := Read()
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	result, err := readTool.Execute(context.Background(), inputJSON, tool.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return result.Output
}

func TestReadTool_TruncatesByBytes(t *testing.T) {
	tmpDir := t.TempDir()
	largePath := filepath.Join(tmpDir, "large.txt")

	// Create content with multiple lines that together exceed 50KB
	// Each line is 100 chars, so we need >500 lines to exceed 50KB
	var lines []string
	for i := 0; i < 600; i++ {
		lines = append(lines, strings.Repeat("x", 100))
	}
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(largePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeReadTool(t, ReadInput{FilePath: largePath})

	if !strings.Contains(result, "Output truncated at") {
		t.Errorf("expected truncation message for large file, got:\n%s", result[max(0, len(result)-500):])
	}
	if !strings.Contains(result, "bytes") {
		t.Error("expected bytes truncation message")
	}
}

func TestReadTool_TruncatesByLineCount(t *testing.T) {
	tmpDir := t.TempDir()
	manyLinesPath := filepath.Join(tmpDir, "many-lines.txt")

	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line"+string(rune('0'+i%10)))
	}
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(manyLinesPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeReadTool(t, ReadInput{FilePath: manyLinesPath, Limit: 10})

	if !strings.Contains(result, "File has more lines") {
		t.Error("expected 'File has more lines' message")
	}
	if !strings.Contains(result, "line0") {
		t.Error("expected first line to be present")
	}
}

func TestReadTool_DoesNotTruncateSmallFile(t *testing.T) {
	tmpDir := t.TempDir()
	smallPath := filepath.Join(tmpDir, "small.txt")

	if err := os.WriteFile(smallPath, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeReadTool(t, ReadInput{FilePath: smallPath})

	if !strings.Contains(result, "End of file") {
		t.Error("expected 'End of file' message for small file")
	}
	if !strings.Contains(result, "hello world") {
		t.Error("expected content to be present")
	}
}

func TestReadTool_RespectsOffset(t *testing.T) {
	tmpDir := t.TempDir()
	offsetPath := filepath.Join(tmpDir, "offset.txt")

	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "lineNUM"+string(rune('A'+i)))
	}
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(offsetPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeReadTool(t, ReadInput{FilePath: offsetPath, Offset: 10, Limit: 5})

	// Line numbers are 1-based, offset is 0-based, so offset 10 = line 11
	if !strings.Contains(result, "00011|") {
		t.Errorf("expected line 11 to be present (offset 10), got:\n%s", result)
	}
	if strings.Contains(result, "00001|") {
		t.Error("expected line 1 to NOT be present with offset 10")
	}
	if !strings.Contains(result, "lineNUMK") {
		t.Error("expected content at offset 10 (lineNUMK)")
	}
}

func TestReadTool_TruncatesLongLines(t *testing.T) {
	tmpDir := t.TempDir()
	longLinePath := filepath.Join(tmpDir, "long-line.txt")

	longLine := strings.Repeat("x", 3000)
	if err := os.WriteFile(longLinePath, []byte(longLine), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeReadTool(t, ReadInput{FilePath: longLinePath})

	if !strings.Contains(result, "...") {
		t.Error("expected truncated long line with ...")
	}
	if len(result) >= 3000 {
		t.Error("expected result to be shorter than original long line")
	}
}

func TestReadTool_FileNotFound(t *testing.T) {
	result := executeReadTool(t, ReadInput{FilePath: "/nonexistent/path/file.txt"})

	if !strings.Contains(result, "File not found") {
		t.Errorf("expected 'File not found' message, got: %s", result)
	}
}

func TestReadTool_BinaryFileByExtension(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "file.exe")

	if err := os.WriteFile(binaryPath, []byte{0x4D, 0x5A, 0x90}, 0644); err != nil {
		t.Fatal(err)
	}

	result := executeReadTool(t, ReadInput{FilePath: binaryPath})

	if !strings.Contains(result, "Cannot read binary file") {
		t.Errorf("expected binary file rejection message, got: %s", result)
	}
}

func TestReadTool_LineNumberFormat(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(path, []byte("line1\nline2\nline3"), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeReadTool(t, ReadInput{FilePath: path})

	// Check opencode format: 5-digit padded + "| "
	if !strings.Contains(result, "00001| line1") {
		t.Errorf("expected opencode line number format (00001| ), got:\n%s", result)
	}
	if !strings.Contains(result, "00002| line2") {
		t.Error("expected opencode line number format (00002| )")
	}
}

func TestReadTool_WrappedInFileTags(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeReadTool(t, ReadInput{FilePath: path})

	if !strings.HasPrefix(result, "<file>\n") {
		t.Errorf("expected output to start with <file> tag, got:\n%s", result)
	}
	if !strings.HasSuffix(result, "</file>") {
		t.Error("expected output to end with </file> tag")
	}
}

func TestIsBinaryByExtension(t *testing.T) {
	cases := []struct {
		path   string
		binary bool
	}{
		{"/path/to/file.exe", true},
		{"/path/to/file.dll", true},
		{"/path/to/file.zip", true},
		{"/path/to/file.txt", false},
		{"/path/to/file.go", false},
		{"/path/to/file.js", false},
		{"/path/to/file.wasm", true},
		{"/path/to/file.pyc", true},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			result := isBinaryByExtension(tc.path)
			if result != tc.binary {
				t.Errorf("isBinaryByExtension(%q) = %v, want %v", tc.path, result, tc.binary)
			}
		})
	}
}

func TestIsBinaryContent(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		binary  bool
	}{
		{"empty", []byte{}, false},
		{"text", []byte("hello world"), false},
		{"null byte", []byte{0x00, 0x01, 0x02}, true},
		{"mostly printable", []byte("hello\nworld\ttab"), false},
		{"high non-printable", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x0E, 0x0F}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := isBinaryContent(tc.content)
			if result != tc.binary {
				t.Errorf("isBinaryContent(%v) = %v, want %v", tc.content, result, tc.binary)
			}
		})
	}
}
