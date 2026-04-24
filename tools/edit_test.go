package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
)

func setupTestPermissionHandler() (context.Context, func()) {
	b := bus.New()
	pm := bus.NewPermissionManager(b)
	pm.SetRules([]bus.PermissionRule{
		{Permission: "*", Pattern: "*", Action: "allow"},
	})
	ctx := bus.WithPermissionManager(context.Background(), pm)
	return ctx, func() {}
}

func executeEditTool(t *testing.T, input EditInput) string {
	t.Helper()

	ctx, cleanup := setupTestPermissionHandler()
	defer cleanup()

	editTool := Edit()
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	result, err := editTool.Execute(ctx, inputJSON, tool.CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return result.Output
}

func TestEditTool_SimpleReplacement(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeEditTool(t, EditInput{
		FilePath:  path,
		OldString: "hello",
		NewString: "goodbye",
	})

	if !strings.Contains(result, "successfully") {
		t.Errorf("expected success message, got: %s", result)
	}

	content, _ := os.ReadFile(path)
	if string(content) != "goodbye world" {
		t.Errorf("expected 'goodbye world', got: %s", content)
	}
}

func TestEditTool_ReplaceAll(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(path, []byte("foo bar foo baz foo"), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeEditTool(t, EditInput{
		FilePath:   path,
		OldString:  "foo",
		NewString:  "qux",
		ReplaceAll: true,
	})

	if !strings.Contains(result, "successfully") {
		t.Errorf("expected success message, got: %s", result)
	}

	content, _ := os.ReadFile(path)
	if string(content) != "qux bar qux baz qux" {
		t.Errorf("expected 'qux bar qux baz qux', got: %s", content)
	}
}

func TestEditTool_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeEditTool(t, EditInput{
		FilePath:  path,
		OldString: "notfound",
		NewString: "replacement",
	})

	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' error, got: %s", result)
	}
}

func TestEditTool_MultipleMatchesWithoutReplaceAll(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(path, []byte("foo bar foo"), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeEditTool(t, EditInput{
		FilePath:  path,
		OldString: "foo",
		NewString: "baz",
	})

	if !strings.Contains(result, "multiple matches") {
		t.Errorf("expected 'multiple matches' error, got: %s", result)
	}
}

func TestEditTool_SameOldNewString(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeEditTool(t, EditInput{
		FilePath:  path,
		OldString: "hello",
		NewString: "hello",
	})

	if !strings.Contains(result, "must be different") {
		t.Errorf("expected 'must be different' error, got: %s", result)
	}
}

func TestEditTool_FileNotFound(t *testing.T) {
	result := executeEditTool(t, EditInput{
		FilePath:  "/nonexistent/path/file.txt",
		OldString: "old",
		NewString: "new",
	})

	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' error, got: %s", result)
	}
}

func TestEditTool_CreateNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "new.txt")

	// File doesn't exist yet
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	result := executeEditTool(t, EditInput{
		FilePath:  path,
		OldString: "",
		NewString: "new content",
	})

	if !strings.Contains(result, "successfully") {
		t.Errorf("expected success message, got: %s", result)
	}

	content, _ := os.ReadFile(path)
	if string(content) != "new content" {
		t.Errorf("expected 'new content', got: %s", content)
	}
}

// Replacer strategy tests

func TestSimpleReplacer(t *testing.T) {
	content := "hello world"
	matches := SimpleReplacer(content, "hello")

	if len(matches) != 1 || matches[0] != "hello" {
		t.Errorf("expected ['hello'], got: %v", matches)
	}

	// No match
	matches = SimpleReplacer(content, "xyz")
	if len(matches) != 0 {
		t.Errorf("expected empty, got: %v", matches)
	}
}

func TestLineTrimmedReplacer(t *testing.T) {
	content := "  hello  \n  world  \n"
	matches := LineTrimmedReplacer(content, "hello\nworld")

	if len(matches) != 1 {
		t.Errorf("expected 1 match, got: %d", len(matches))
	}

	// Should match the actual lines with whitespace
	if !strings.Contains(matches[0], "hello") || !strings.Contains(matches[0], "world") {
		t.Errorf("expected match to contain both lines, got: %q", matches[0])
	}
}

func TestBlockAnchorReplacer(t *testing.T) {
	content := `function foo() {
    // some code
    // more code
    return true;
}`
	find := `function foo() {
    // different middle
    return true;
}`

	matches := BlockAnchorReplacer(content, find)

	// Should match based on first and last line anchors
	if len(matches) != 1 {
		t.Errorf("expected 1 match, got: %d - matches: %v", len(matches), matches)
	}
}

func TestWhitespaceNormalizedReplacer(t *testing.T) {
	content := "hello    world"
	matches := WhitespaceNormalizedReplacer(content, "hello world")

	if len(matches) == 0 {
		t.Error("expected match with normalized whitespace")
	}
}

func TestIndentationFlexibleReplacer(t *testing.T) {
	content := `    function foo() {
        return true;
    }`
	find := `function foo() {
    return true;
}`

	matches := IndentationFlexibleReplacer(content, find)

	if len(matches) != 1 {
		t.Errorf("expected 1 match with flexible indentation, got: %d", len(matches))
	}
}

func TestTrimmedBoundaryReplacer(t *testing.T) {
	content := "hello world"
	matches := TrimmedBoundaryReplacer(content, "  hello world  ")

	if len(matches) == 0 {
		t.Error("expected match with trimmed boundaries")
	}
}

func TestMultiOccurrenceReplacer(t *testing.T) {
	content := "foo bar foo baz foo"
	matches := MultiOccurrenceReplacer(content, "foo")

	if len(matches) != 3 {
		t.Errorf("expected 3 matches, got: %d", len(matches))
	}
}

func TestReplace_SimpleCase(t *testing.T) {
	content := "hello world"
	result, err := replace(content, "hello", "goodbye", false)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "goodbye world" {
		t.Errorf("expected 'goodbye world', got: %s", result)
	}
}

func TestReplace_WithTrimmedWhitespace(t *testing.T) {
	content := "  hello  \n  world  \n"
	// This should match using LineTrimmedReplacer
	result, err := replace(content, "hello\nworld", "foo\nbar", false)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "foo") || !strings.Contains(result, "bar") {
		t.Errorf("expected replacement with foo and bar, got: %s", result)
	}
}

func TestReplace_MultipleMatchesError(t *testing.T) {
	content := "foo bar foo"
	_, err := replace(content, "foo", "baz", false)

	if err == nil {
		t.Error("expected error for multiple matches")
	}
	if !strings.Contains(err.Error(), "multiple matches") {
		t.Errorf("expected 'multiple matches' error, got: %v", err)
	}
}

func TestReplace_NotFoundError(t *testing.T) {
	content := "hello world"
	_, err := replace(content, "xyz", "abc", false)

	if err == nil {
		t.Error("expected error for not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestReplace_ReplaceAll(t *testing.T) {
	content := "foo bar foo baz foo"
	result, err := replace(content, "foo", "qux", true)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != "qux bar qux baz qux" {
		t.Errorf("expected 'qux bar qux baz qux', got: %s", result)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b     string
		expected int
	}{
		{"", "", 0},
		{"hello", "", 5},
		{"", "world", 5},
		{"hello", "hello", 0},
		{"hello", "hallo", 1},
		{"kitten", "sitting", 3},
	}

	for _, tc := range cases {
		t.Run(tc.a+"_"+tc.b, func(t *testing.T) {
			result := levenshtein(tc.a, tc.b)
			if result != tc.expected {
				t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, result, tc.expected)
			}
		})
	}
}

func TestNormalizeLineEndings(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"hello\r\nworld", "hello\nworld"},
		{"hello\nworld", "hello\nworld"},
		{"hello\r\nworld\r\n", "hello\nworld\n"},
		{"no crlf", "no crlf"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			result := normalizeLineEndings(tc.input)
			if result != tc.expected {
				t.Errorf("normalizeLineEndings(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}
