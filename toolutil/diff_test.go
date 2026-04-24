package toolutil

import (
	"strings"
	"testing"
)

func TestGenerateDiff_Addition(t *testing.T) {
	old := "line1\nline2\nline3"
	new := "line1\nline2\nnewline\nline3"

	diff := GenerateDiff("test.txt", old, new)

	if !strings.Contains(diff, "--- test.txt") {
		t.Error("diff should contain old file header")
	}
	if !strings.Contains(diff, "+++ test.txt") {
		t.Error("diff should contain new file header")
	}
	if !strings.Contains(diff, "+newline") {
		t.Error("diff should show added line")
	}
}

func TestGenerateDiff_Deletion(t *testing.T) {
	old := "line1\nline2\nline3"
	new := "line1\nline3"

	diff := GenerateDiff("test.txt", old, new)

	if !strings.Contains(diff, "-line2") {
		t.Error("diff should show removed line")
	}
}

func TestGenerateDiff_Modification(t *testing.T) {
	old := "line1\nold content\nline3"
	new := "line1\nnew content\nline3"

	diff := GenerateDiff("test.txt", old, new)

	if !strings.Contains(diff, "-old content") {
		t.Error("diff should show removed line")
	}
	if !strings.Contains(diff, "+new content") {
		t.Error("diff should show added line")
	}
}

func TestGenerateDiff_NoChange(t *testing.T) {
	content := "line1\nline2\nline3"

	diff := GenerateDiff("test.txt", content, content)

	// Should still have headers but no hunks
	if !strings.Contains(diff, "--- test.txt") {
		t.Error("diff should contain headers even with no changes")
	}
}

func TestGenerateDiff_NewFile(t *testing.T) {
	old := ""
	new := "line1\nline2\nline3"

	diff := GenerateDiff("test.txt", old, new)

	if !strings.Contains(diff, "+line1") {
		t.Error("diff should show all lines as additions for new file")
	}
}

func TestTrimDiff(t *testing.T) {
	// Create a long diff
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "+added line")
	}
	longDiff := strings.Join(lines, "\n")

	trimmed := TrimDiff(longDiff, 20)

	// Should be shorter than original
	trimmedLines := strings.Split(trimmed, "\n")
	if len(trimmedLines) >= 100 {
		t.Error("trimmed diff should be shorter")
	}

	// Should contain truncation message
	if !strings.Contains(trimmed, "more lines") {
		t.Error("trimmed diff should indicate truncation")
	}
}

func TestTrimDiff_ShortDiff(t *testing.T) {
	shortDiff := "line1\nline2\nline3"

	trimmed := TrimDiff(shortDiff, 20)

	// Should be unchanged
	if trimmed != shortDiff {
		t.Error("short diff should not be modified")
	}
}
