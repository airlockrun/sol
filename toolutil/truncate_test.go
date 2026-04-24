package toolutil

import (
	"strings"
	"testing"
)

func TestTruncateOutput_UnderLimits(t *testing.T) {
	content := "line1\nline2\nline3"
	result := TruncateOutput(content, TruncateOptions{})

	if result.Truncated {
		t.Error("expected not truncated for small content")
	}
	if result.Content != content {
		t.Errorf("expected unchanged content, got: %s", result.Content)
	}
}

func TestTruncateOutput_ByLineCount(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line")
	}
	content := strings.Join(lines, "\n")

	result := TruncateOutput(content, TruncateOptions{MaxLines: 10})

	if !result.Truncated {
		t.Error("expected truncated")
	}
	if !strings.Contains(result.Content, "90 lines truncated") {
		t.Errorf("expected '90 lines truncated', got: %s", result.Content)
	}
}

func TestTruncateOutput_ByByteCount(t *testing.T) {
	content := strings.Repeat("a", 1000)

	result := TruncateOutput(content, TruncateOptions{MaxBytes: 100})

	if !result.Truncated {
		t.Error("expected truncated")
	}
	if !strings.Contains(result.Content, "truncated") {
		t.Errorf("expected truncation message, got: %s", result.Content)
	}
}

func TestTruncateOutput_HeadDirection(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, "line"+string(rune('0'+i)))
	}
	content := strings.Join(lines, "\n")

	result := TruncateOutput(content, TruncateOptions{MaxLines: 3, Direction: "head"})

	if !result.Truncated {
		t.Error("expected truncated")
	}
	if !strings.Contains(result.Content, "line0") {
		t.Error("expected line0 in head truncation")
	}
	if !strings.Contains(result.Content, "line1") {
		t.Error("expected line1 in head truncation")
	}
	if !strings.Contains(result.Content, "line2") {
		t.Error("expected line2 in head truncation")
	}
	// line9 should be truncated
	if strings.Contains(result.Content, "line9") && !strings.Contains(result.Content, "truncated") {
		t.Error("expected line9 to not be in head truncation result")
	}
}

func TestTruncateOutput_TailDirection(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, "line"+string(rune('0'+i)))
	}
	content := strings.Join(lines, "\n")

	result := TruncateOutput(content, TruncateOptions{MaxLines: 3, Direction: "tail"})

	if !result.Truncated {
		t.Error("expected truncated")
	}
	if !strings.Contains(result.Content, "line7") {
		t.Error("expected line7 in tail truncation")
	}
	if !strings.Contains(result.Content, "line8") {
		t.Error("expected line8 in tail truncation")
	}
	if !strings.Contains(result.Content, "line9") {
		t.Error("expected line9 in tail truncation")
	}
}

func TestTruncateOutput_SavesFullOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line"+string(rune('0'+i%10)))
	}
	content := strings.Join(lines, "\n")

	result := TruncateOutput(content, TruncateOptions{MaxLines: 10})

	if !result.Truncated {
		t.Error("expected truncated")
	}
	if result.OutputPath == "" {
		t.Error("expected output path when truncated")
	}
	if !strings.Contains(result.Content, "truncated") {
		t.Error("expected truncation message")
	}
	if !strings.Contains(result.Content, "Grep") {
		t.Error("expected hint about using Grep")
	}
}

func TestTruncateOutput_DefaultValues(t *testing.T) {
	if TruncateMaxLines != 2000 {
		t.Errorf("expected MAX_LINES=2000, got: %d", TruncateMaxLines)
	}
	if TruncateMaxBytes != 50*1024 {
		t.Errorf("expected MAX_BYTES=51200, got: %d", TruncateMaxBytes)
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"hello", []string{"hello"}},
		{"hello\nworld", []string{"hello", "world"}},
		{"hello\nworld\n", []string{"hello", "world"}},
		{"a\nb\nc", []string{"a", "b", "c"}},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			result := splitLines(tc.input)
			if len(result) != len(tc.expected) {
				t.Errorf("splitLines(%q) = %v, want %v", tc.input, result, tc.expected)
				return
			}
			for i := range result {
				if result[i] != tc.expected[i] {
					t.Errorf("splitLines(%q)[%d] = %q, want %q", tc.input, i, result[i], tc.expected[i])
				}
			}
		})
	}
}

func TestJoinLines(t *testing.T) {
	cases := []struct {
		input    []string
		expected string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"hello"}, "hello"},
		{[]string{"hello", "world"}, "hello\nworld"},
		{[]string{"a", "b", "c"}, "a\nb\nc"},
	}

	for _, tc := range cases {
		t.Run(tc.expected, func(t *testing.T) {
			result := joinLines(tc.input)
			if result != tc.expected {
				t.Errorf("joinLines(%v) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}
