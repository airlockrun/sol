// Package toolutil provides utilities used by tools.
package toolutil

import (
	"fmt"
	"strings"
)

// GenerateDiff creates a unified diff between old and new content
func GenerateDiff(filename, oldContent, newContent string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	// Simple unified diff generation
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- %s\n", filename))
	sb.WriteString(fmt.Sprintf("+++ %s\n", filename))

	// Find differences using a simple LCS-based approach
	hunks := generateHunks(oldLines, newLines)
	for _, hunk := range hunks {
		sb.WriteString(hunk)
	}

	return sb.String()
}

// generateHunks generates diff hunks
func generateHunks(oldLines, newLines []string) []string {
	var hunks []string

	// Simple line-by-line diff with context
	const contextLines = 3

	// Find matching and differing sections
	type change struct {
		oldStart, oldCount int
		newStart, newCount int
		oldContent         []string
		newContent         []string
	}

	var changes []change
	i, j := 0, 0

	for i < len(oldLines) || j < len(newLines) {
		// Skip matching lines
		for i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
			i++
			j++
		}

		// Check if we've reached the end
		if i >= len(oldLines) && j >= len(newLines) {
			break
		}

		// Find the extent of the difference
		oldChangeStart := i
		newChangeStart := j

		// Look for next matching section
		found := false
		for di := 0; di < 10 && i+di < len(oldLines) && !found; di++ {
			for dj := 0; dj < 10 && j+dj < len(newLines) && !found; dj++ {
				if oldLines[i+di] == newLines[j+dj] {
					// Found a match - record the change
					changes = append(changes, change{
						oldStart:   oldChangeStart,
						oldCount:   di,
						newStart:   newChangeStart,
						newCount:   dj,
						oldContent: oldLines[oldChangeStart : oldChangeStart+di],
						newContent: newLines[newChangeStart : newChangeStart+dj],
					})
					i += di
					j += dj
					found = true
				}
			}
		}

		if !found {
			// No match found in lookahead - consume remaining lines
			changes = append(changes, change{
				oldStart:   oldChangeStart,
				oldCount:   len(oldLines) - oldChangeStart,
				newStart:   newChangeStart,
				newCount:   len(newLines) - newChangeStart,
				oldContent: oldLines[oldChangeStart:],
				newContent: newLines[newChangeStart:],
			})
			break
		}
	}

	// Generate hunks from changes
	for _, c := range changes {
		if c.oldCount == 0 && c.newCount == 0 {
			continue
		}

		var hunk strings.Builder

		// Calculate context boundaries
		contextStart := c.oldStart - contextLines
		if contextStart < 0 {
			contextStart = 0
		}

		// Hunk header
		oldHunkStart := contextStart + 1 // 1-indexed
		oldHunkLen := c.oldCount + min(contextLines, c.oldStart) + min(contextLines, len(oldLines)-c.oldStart-c.oldCount)
		newHunkStart := c.newStart - (c.oldStart - contextStart) + 1
		newHunkLen := c.newCount + min(contextLines, c.newStart) + min(contextLines, len(newLines)-c.newStart-c.newCount)

		hunk.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldHunkStart, oldHunkLen, newHunkStart, newHunkLen))

		// Context before
		for k := contextStart; k < c.oldStart && k < len(oldLines); k++ {
			hunk.WriteString(" " + oldLines[k] + "\n")
		}

		// Removed lines
		for _, line := range c.oldContent {
			hunk.WriteString("-" + line + "\n")
		}

		// Added lines
		for _, line := range c.newContent {
			hunk.WriteString("+" + line + "\n")
		}

		// Context after
		contextEnd := c.oldStart + c.oldCount + contextLines
		if contextEnd > len(oldLines) {
			contextEnd = len(oldLines)
		}
		for k := c.oldStart + c.oldCount; k < contextEnd; k++ {
			hunk.WriteString(" " + oldLines[k] + "\n")
		}

		hunks = append(hunks, hunk.String())
	}

	return hunks
}

// TrimDiff trims a diff to a reasonable size for display
func TrimDiff(diff string, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 50
	}

	lines := strings.Split(diff, "\n")
	if len(lines) <= maxLines {
		return diff
	}

	// Keep header and first/last parts
	result := strings.Join(lines[:maxLines-3], "\n")
	result += fmt.Sprintf("\n... (%d more lines) ...\n", len(lines)-maxLines)
	result += strings.Join(lines[len(lines)-3:], "\n")
	return result
}
