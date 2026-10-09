package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestReadTool_ExtensionlessFile guards the isBinaryByExtension panic: a path
// with no "." (Dockerfile, Makefile, LICENSE) made strings.LastIndex return -1
// and the old path[-1:] slice blew up. Such files are text and must read fine.
func TestReadTool_ExtensionlessFile(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "Dockerfile")
	if err := os.WriteFile(p, []byte("FROM scratch\nCMD [\"/agent\"]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result := executeReadTool(t, ReadInput{FilePath: p})
	if !strings.Contains(result, "FROM scratch") {
		t.Errorf("expected Dockerfile contents, got:\n%s", result)
	}
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

	if !strings.Contains(result, "Output capped at") {
		t.Errorf("expected byte-cap message for large file, got:\n%s", result[max(0, len(result)-500):])
	}
	// The footer must hand the model an explicit offset to continue from.
	if !strings.Contains(result, "Use offset=") {
		t.Error("expected an explicit 'Use offset=' continuation hint")
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

	// 10 of 100 lines shown, with the offset to continue from line 10 (0-based).
	if !strings.Contains(result, "Showing lines 1-10 of 100") {
		t.Errorf("expected 'Showing lines 1-10 of 100' range, got:\n%s", result[max(0, len(result)-300):])
	}
	if !strings.Contains(result, "Use offset=10 to continue.") {
		t.Error("expected 'Use offset=10 to continue.' hint")
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

func TestReadTool_DirectoryKeepsFileError(t *testing.T) {
	path := t.TempDir()
	input, _ := json.Marshal(ReadInput{FilePath: path})
	result, err := Read().Execute(t.Context(), input, tool.CallOptions{})
	if err != nil || result.Output != "Error: File not found: "+path || len(result.Attachments) != 0 {
		t.Fatalf("directory result = %+v, error = %v", result, err)
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

// TestReadTool_TrailingNewlineLineCount: a newline-terminated file (the common
// case) must not report an off-by-one total or render a phantom blank final line.
func TestReadTool_TrailingNewlineLineCount(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "nl.txt")
	if err := os.WriteFile(p, []byte("a\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result := executeReadTool(t, ReadInput{FilePath: p})

	if !strings.Contains(result, "total 2 lines") {
		t.Errorf("expected 'total 2 lines' for a 2-line file, got:\n%s", result)
	}
	if strings.Contains(result, "00003|") {
		t.Errorf("phantom empty final line rendered:\n%s", result)
	}
	if !strings.Contains(result, "00001| a") || !strings.Contains(result, "00002| b") {
		t.Errorf("expected both content lines, got:\n%s", result)
	}
}

// TestReadTool_LongLineTruncationIsValidUTF8: truncating a long line of
// multibyte runes must cut on a rune boundary, not mid-rune. The three-byte
// rune here makes byte 2000 land inside a rune, so a raw byte slice would emit
// invalid UTF-8.
func TestReadTool_LongLineTruncationIsValidUTF8(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "wide.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("€", 3000)), 0644); err != nil {
		t.Fatal(err)
	}
	result := executeReadTool(t, ReadInput{FilePath: p})

	if !strings.Contains(result, "...") {
		t.Error("expected the long line to be truncated with ...")
	}
	if !utf8.ValidString(result) {
		t.Error("truncated output is not valid UTF-8 — a rune was split")
	}
}

func TestReadTool_DescriptionMatchesBehavior(t *testing.T) {
	desc := Read().Description
	for _, required := range []string{"native image attachments", "PNG, JPEG, GIF, and WebP", "5 MiB", "Omit offset and limit"} {
		if !strings.Contains(desc, required) {
			t.Errorf("description omits %q", required)
		}
	}
	if strings.Contains(desc, "system reminder") {
		t.Error("description promises an unimplemented empty-file reminder")
	}
}

func readTestImage(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&encoded, img)
	case "jpeg":
		err = jpeg.Encode(&encoded, img, nil)
	case "gif":
		err = gif.Encode(&encoded, img, nil)
	case "webp":
		data, decodeErr := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if _, _, decodeErr := image.Decode(bytes.NewReader(data)); decodeErr != nil {
			t.Fatalf("invalid WebP test fixture: %v", decodeErr)
		}
		return data
	default:
		t.Fatalf("unsupported test image format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestReadTool_NativeImagesThroughExecutor(t *testing.T) {
	for _, tc := range []struct {
		name, format, filename, mimeType string
	}{
		{"png", "png", "image.png", "image/png"},
		{"jpeg with misleading extension", "jpeg", "image.png", "image/jpeg"},
		{"gif", "gif", "image.gif", "image/gif"},
		{"webp", "webp", "image.webp", "image/webp"},
		{"extensionless", "png", "image", "image/png"},
		{"binary extension", "png", "image.bin", "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := readTestImage(t, tc.format)
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, tc.filename), data, 0600); err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(ReadInput{FilePath: tc.filename})
			executor := tool.NewLocalExecutor(tool.Set{"read": Read()}, nil)
			response, err := executor.Execute(t.Context(), tool.Request{ToolCallID: "image-read", ToolName: "read", WorkDir: root, Input: input})
			if err != nil || response.IsError || len(response.Attachments) != 1 {
				t.Fatalf("response = %+v, error = %v", response, err)
			}
			attachment := response.Attachments[0]
			decoded, err := base64.StdEncoding.DecodeString(attachment.Data)
			if err != nil || !bytes.Equal(decoded, data) || attachment.MimeType != tc.mimeType || attachment.Filename != tc.filename {
				t.Fatal("executor lost original image bytes, MIME type or filename")
			}
			if !strings.Contains(response.Output, "Image attached:") || strings.Contains(response.Output, attachment.Data) {
				t.Fatal("image was flattened into text output")
			}
			if response.Metadata["width"] != 1 || response.Metadata["height"] != 1 || response.Metadata["bytes"] != len(data) {
				t.Fatalf("image metadata = %+v", response.Metadata)
			}
		})
	}
}

func TestReadTool_ImageRejectsPaginationAndMalformedHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, parameters, wantError string
		data                        []byte
	}{
		{"offset", `,"offset":1`, "omit both", readTestImage(t, "png")},
		{"explicit zero offset", `,"offset":0`, "omit both", readTestImage(t, "png")},
		{"limit", `,"limit":1`, "omit both", readTestImage(t, "png")},
		{"explicit zero limit", `,"limit":0`, "omit both", readTestImage(t, "png")},
		{"explicit null", `,"offset":null`, "omit both", readTestImage(t, "png")},
		{"negative offset", `,"offset":-1`, "omit both", readTestImage(t, "png")},
		{"truncated PNG", "", "invalid image/png image", []byte("\x89PNG\r\n\x1a\n")},
		{"truncated JPEG", "", "invalid image/jpeg image", []byte{0xff, 0xd8, 0xff}},
		{"truncated GIF", "", "invalid image/gif image", []byte("GIF89a")},
		{"truncated WebP", "", "invalid image/webp image", []byte("RIFF\x10\x00\x00\x00WEBPVP8 ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.png")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			pathJSON, _ := json.Marshal(path)
			input := json.RawMessage(`{"filePath":` + string(pathJSON) + tc.parameters + `}`)
			executor := tool.NewLocalExecutor(tool.Set{"read": Read()}, nil)
			response, err := executor.Execute(t.Context(), tool.Request{ToolName: "read", Input: input})
			if err != nil || !response.IsError || !strings.Contains(response.Error, tc.wantError) || len(response.Attachments) != 0 {
				t.Fatalf("response = %+v, error = %v", response, err)
			}
		})
	}
}

func TestReadTool_ImageSizeLimitDoesNotLimitText(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "at limit", true: "above limit"}[oversized], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.png")
			data := readTestImage(t, "png")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			size := int64(maxReadImageBytes)
			if oversized {
				size++
			}
			if err := os.Truncate(path, size); err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(ReadInput{FilePath: path})
			response, err := tool.NewLocalExecutor(tool.Set{"read": Read()}, nil).Execute(t.Context(), tool.Request{ToolName: "read", Input: input})
			if err != nil || response.IsError != oversized {
				t.Fatalf("response error = %q, execute error = %v", response.Error, err)
			}
			if oversized {
				if !strings.Contains(response.Error, "5 MiB (5242880 bytes)") || len(response.Attachments) != 0 {
					t.Fatal("oversized image did not produce a bounded, clear error")
				}
			} else if len(response.Attachments) != 1 || response.Metadata["bytes"] != maxReadImageBytes {
				t.Fatal("image at the documented limit was rejected")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "large.png")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxReadImageBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if got := executeReadTool(t, ReadInput{FilePath: path}); !strings.Contains(got, "00001|") || strings.Contains(got, "image") {
		t.Fatal("text with an image extension was subjected to the image size limit")
	}
}

type readImageZeroReader struct{}

func (readImageZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type readImageCountingReader struct {
	io.Reader
	bytes int
}

func (r *readImageCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}

func TestReadImage_BoundsGrowingFile(t *testing.T) {
	data := readTestImage(t, "png")
	path := filepath.Join(t.TempDir(), "growing.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := &readImageCountingReader{Reader: io.MultiReader(bytes.NewReader(data), readImageZeroReader{})}
	result, err := readImage(t.Context(), file, reader, path, "image/png")
	if err == nil || !strings.Contains(err.Error(), "read limit") || len(result.Attachments) != 0 || reader.bytes != maxReadImageBytes+1 {
		t.Fatalf("error = %v, bytes read = %d, attachments = %d", err, reader.bytes, len(result.Attachments))
	}
}

func TestReadTool_UnsupportedImagesRemainBinary(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"BMP", []byte("BM\x00\x00\x00\x00\x00\x00")},
		{"TIFF", []byte("II\x2a\x00\x08\x00\x00\x00")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unsupported.png")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(ReadInput{FilePath: path})
			result, err := Read().Execute(t.Context(), input, tool.CallOptions{})
			if err != nil || len(result.Attachments) != 0 || !strings.Contains(result.Output, "Cannot read binary file") {
				t.Fatalf("unsupported image response = %+v, error = %v", result, err)
			}
		})
	}
}
