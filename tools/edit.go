package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/toolutil"
)

// EditInput is the input schema for the edit tool
type EditInput struct {
	FilePath   string `json:"filePath" description:"The absolute path to the file to modify"`
	OldString  string `json:"oldString" description:"The text to replace"`
	NewString  string `json:"newString" description:"The text to replace it with (must be different from oldString)"`
	ReplaceAll bool   `json:"replaceAll,omitempty" description:"Replace all occurrences of oldString (default false)"`
}

// Edit creates the edit tool
func Edit() tool.Tool {
	return tool.New("edit").
		Description("Performs exact string replacements in files. \n\nUsage:\n- You must use your `Read` tool at least once in the conversation before editing. This tool will error if you attempt an edit without reading the file. \n- When editing text from Read tool output, ensure you preserve the exact indentation (tabs/spaces) as it appears AFTER the line number prefix. The line number prefix format is: spaces + line number + tab. Everything after that tab is the actual file content to match. Never include any part of the line number prefix in the oldString or newString.\n- ALWAYS prefer editing existing files in the codebase. NEVER write new files unless explicitly required.\n- Only use emojis if the user explicitly requests it. Avoid adding emojis to files unless asked.\n- The edit will FAIL if `oldString` is not found in the file with an error \"oldString not found in content\".\n- The edit will FAIL if `oldString` is found multiple times in the file with an error \"oldString found multiple times and requires more code context to uniquely identify the intended match\". Either provide a larger string with more surrounding context to make it unique or use `replaceAll` to change every instance of `oldString`. \n- Use `replaceAll` for replacing and renaming strings across the file. This parameter is useful if you want to rename a variable for instance.\n").
		SchemaFromStruct(EditInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args EditInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			// Get session ID for file time tracking
			sessionID, _ := ctx.Value(SessionIDKey).(string)

			if args.OldString == args.NewString {
				return tool.Result{
					Output: "oldString and newString must be different",
					Title:  filepath.Base(args.FilePath),
				}, nil
			}

			content, err := os.ReadFile(args.FilePath)
			if err != nil {
				return tool.Result{
					Output: fmt.Sprintf("File %s not found", args.FilePath),
					Title:  filepath.Base(args.FilePath),
				}, nil
			}

			// Check file hasn't been modified since last read
			if sessionID != "" {
				if err := toolutil.FileTime.Assert(sessionID, args.FilePath); err != nil {
					return tool.Result{
						Output: fmt.Sprintf("Error: %v", err),
						Title:  filepath.Base(args.FilePath),
					}, nil
				}
			}

			oldContent := normalizeLineEndings(string(content))

			// Get working directory for relative path
			workDir, _ := ctx.Value(WorkDirKey).(string)
			if workDir == "" {
				workDir, _ = os.Getwd()
			}
			relPath, _ := filepath.Rel(workDir, args.FilePath)
			if relPath == "" {
				relPath = args.FilePath
			}

			// Handle empty oldString (create new file)
			if args.OldString == "" {
				// Generate diff and request permission
				diff := toolutil.GenerateDiff(args.FilePath, oldContent, args.NewString)
				permErr := AskPermission(ctx, PermissionRequest{
					SessionID:  sessionID,
					Permission: "edit",
					Patterns:   []string{relPath},
					Always:     []string{"*"},
					Metadata: map[string]any{
						"filepath": args.FilePath,
						"diff":     toolutil.TrimDiff(diff, 50),
					},
				})
				if permErr != nil {
					return tool.Result{
						Output: fmt.Sprintf("Error: %v", permErr),
						Title:  filepath.Base(args.FilePath),
					}, nil
				}

				if err := os.WriteFile(args.FilePath, []byte(args.NewString), 0644); err != nil {
					return tool.Result{
						Output: fmt.Sprintf("Error writing file: %v", err),
						Title:  filepath.Base(args.FilePath),
					}, nil
				}
				// Update file time after successful write
				if sessionID != "" {
					toolutil.FileTime.Read(sessionID, args.FilePath)
				}
				return tool.Result{
					Output: "Edit applied successfully.",
					Title:  filepath.Base(args.FilePath),
				}, nil
			}

			// Use multi-strategy replacement (matching opencode)
			newContent, err := replace(oldContent, args.OldString, args.NewString, args.ReplaceAll)
			if err != nil {
				return tool.Result{
					Output: err.Error(),
					Title:  filepath.Base(args.FilePath),
				}, nil
			}

			// Generate diff and request permission
			diff := toolutil.GenerateDiff(args.FilePath, oldContent, newContent)
			permErr := AskPermission(ctx, PermissionRequest{
				SessionID:  sessionID,
				Permission: "edit",
				Patterns:   []string{relPath},
				Always:     []string{"*"},
				Metadata: map[string]any{
					"filepath": args.FilePath,
					"diff":     toolutil.TrimDiff(diff, 50),
				},
			})
			if permErr != nil {
				return tool.Result{
					Output: fmt.Sprintf("Error: %v", permErr),
					Title:  filepath.Base(args.FilePath),
				}, nil
			}

			if err := os.WriteFile(args.FilePath, []byte(newContent), 0644); err != nil {
				return tool.Result{
					Output: fmt.Sprintf("Error writing file: %v", err),
					Title:  filepath.Base(args.FilePath),
				}, nil
			}

			// Update file time after successful write
			if sessionID != "" {
				toolutil.FileTime.Read(sessionID, args.FilePath)
			}

			return tool.Result{
				Output: "Edit applied successfully.",
				Title:  filepath.Base(args.FilePath),
			}, nil
		}).
		Build()
}

func normalizeLineEndings(text string) string {
	return strings.ReplaceAll(text, "\r\n", "\n")
}

// Replacer is a function that yields possible matches for the find string in content
type Replacer func(content, find string) []string

// Similarity thresholds for block anchor fallback matching
const (
	singleCandidateSimilarityThreshold    = 0.0
	multipleCandidatesSimilarityThreshold = 0.3
)

// levenshtein calculates the Levenshtein distance between two strings
func levenshtein(a, b string) int {
	if a == "" || b == "" {
		return int(math.Max(float64(len(a)), float64(len(b))))
	}

	matrix := make([][]int, len(a)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(b)+1)
		matrix[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		matrix[0][j] = j
	}

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			matrix[i][j] = min(
				matrix[i-1][j]+1,
				matrix[i][j-1]+1,
				matrix[i-1][j-1]+cost,
			)
		}
	}
	return matrix[len(a)][len(b)]
}

// SimpleReplacer returns exact matches
func SimpleReplacer(content, find string) []string {
	if strings.Contains(content, find) {
		return []string{find}
	}
	return nil
}

// LineTrimmedReplacer matches lines with trimmed whitespace
func LineTrimmedReplacer(content, find string) []string {
	originalLines := strings.Split(content, "\n")
	searchLines := strings.Split(find, "\n")

	if len(searchLines) > 0 && searchLines[len(searchLines)-1] == "" {
		searchLines = searchLines[:len(searchLines)-1]
	}

	var results []string
	for i := 0; i <= len(originalLines)-len(searchLines); i++ {
		matches := true
		for j := 0; j < len(searchLines); j++ {
			if strings.TrimSpace(originalLines[i+j]) != strings.TrimSpace(searchLines[j]) {
				matches = false
				break
			}
		}
		if matches {
			matchStartIndex := 0
			for k := 0; k < i; k++ {
				matchStartIndex += len(originalLines[k]) + 1
			}
			matchEndIndex := matchStartIndex
			for k := 0; k < len(searchLines); k++ {
				matchEndIndex += len(originalLines[i+k])
				if k < len(searchLines)-1 {
					matchEndIndex++ // newline
				}
			}
			results = append(results, content[matchStartIndex:matchEndIndex])
		}
	}
	return results
}

// BlockAnchorReplacer matches blocks by first and last line anchors with fuzzy middle
func BlockAnchorReplacer(content, find string) []string {
	originalLines := strings.Split(content, "\n")
	searchLines := strings.Split(find, "\n")

	if len(searchLines) < 3 {
		return nil
	}

	if len(searchLines) > 0 && searchLines[len(searchLines)-1] == "" {
		searchLines = searchLines[:len(searchLines)-1]
	}

	firstLineSearch := strings.TrimSpace(searchLines[0])
	lastLineSearch := strings.TrimSpace(searchLines[len(searchLines)-1])
	searchBlockSize := len(searchLines)

	type candidate struct {
		startLine, endLine int
	}
	var candidates []candidate

	for i := 0; i < len(originalLines); i++ {
		if strings.TrimSpace(originalLines[i]) != firstLineSearch {
			continue
		}
		for j := i + 2; j < len(originalLines); j++ {
			if strings.TrimSpace(originalLines[j]) == lastLineSearch {
				candidates = append(candidates, candidate{startLine: i, endLine: j})
				break
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	extractBlock := func(startLine, endLine int) string {
		matchStartIndex := 0
		for k := 0; k < startLine; k++ {
			matchStartIndex += len(originalLines[k]) + 1
		}
		matchEndIndex := matchStartIndex
		for k := startLine; k <= endLine; k++ {
			matchEndIndex += len(originalLines[k])
			if k < endLine {
				matchEndIndex++
			}
		}
		return content[matchStartIndex:matchEndIndex]
	}

	if len(candidates) == 1 {
		c := candidates[0]
		actualBlockSize := c.endLine - c.startLine + 1
		similarity := 0.0
		linesToCheck := min(searchBlockSize-2, actualBlockSize-2)

		if linesToCheck > 0 {
			for j := 1; j < searchBlockSize-1 && j < actualBlockSize-1; j++ {
				originalLine := strings.TrimSpace(originalLines[c.startLine+j])
				searchLine := strings.TrimSpace(searchLines[j])
				maxLen := max(len(originalLine), len(searchLine))
				if maxLen == 0 {
					continue
				}
				distance := levenshtein(originalLine, searchLine)
				similarity += (1.0 - float64(distance)/float64(maxLen)) / float64(linesToCheck)
				if similarity >= singleCandidateSimilarityThreshold {
					break
				}
			}
		} else {
			similarity = 1.0
		}

		if similarity >= singleCandidateSimilarityThreshold {
			return []string{extractBlock(c.startLine, c.endLine)}
		}
		return nil
	}

	// Multiple candidates: find best match
	var bestMatch *candidate
	maxSimilarity := -1.0

	for i := range candidates {
		c := &candidates[i]
		actualBlockSize := c.endLine - c.startLine + 1
		similarity := 0.0
		linesToCheck := min(searchBlockSize-2, actualBlockSize-2)

		if linesToCheck > 0 {
			for j := 1; j < searchBlockSize-1 && j < actualBlockSize-1; j++ {
				originalLine := strings.TrimSpace(originalLines[c.startLine+j])
				searchLine := strings.TrimSpace(searchLines[j])
				maxLen := max(len(originalLine), len(searchLine))
				if maxLen == 0 {
					continue
				}
				distance := levenshtein(originalLine, searchLine)
				similarity += 1.0 - float64(distance)/float64(maxLen)
			}
			similarity /= float64(linesToCheck)
		} else {
			similarity = 1.0
		}

		if similarity > maxSimilarity {
			maxSimilarity = similarity
			bestMatch = c
		}
	}

	if maxSimilarity >= multipleCandidatesSimilarityThreshold && bestMatch != nil {
		return []string{extractBlock(bestMatch.startLine, bestMatch.endLine)}
	}
	return nil
}

// WhitespaceNormalizedReplacer matches with normalized whitespace
func WhitespaceNormalizedReplacer(content, find string) []string {
	normalizeWhitespace := func(text string) string {
		re := regexp.MustCompile(`\s+`)
		return strings.TrimSpace(re.ReplaceAllString(text, " "))
	}

	normalizedFind := normalizeWhitespace(find)
	lines := strings.Split(content, "\n")
	var results []string

	for _, line := range lines {
		if normalizeWhitespace(line) == normalizedFind {
			results = append(results, line)
		} else {
			normalizedLine := normalizeWhitespace(line)
			if strings.Contains(normalizedLine, normalizedFind) {
				words := strings.Fields(strings.TrimSpace(find))
				if len(words) > 0 {
					var escapedWords []string
					for _, w := range words {
						escapedWords = append(escapedWords, regexp.QuoteMeta(w))
					}
					pattern := strings.Join(escapedWords, `\s+`)
					re, err := regexp.Compile(pattern)
					if err == nil {
						if match := re.FindString(line); match != "" {
							results = append(results, match)
						}
					}
				}
			}
		}
	}

	// Handle multi-line matches
	findLines := strings.Split(find, "\n")
	if len(findLines) > 1 {
		for i := 0; i <= len(lines)-len(findLines); i++ {
			block := strings.Join(lines[i:i+len(findLines)], "\n")
			if normalizeWhitespace(block) == normalizedFind {
				results = append(results, block)
			}
		}
	}

	return results
}

// IndentationFlexibleReplacer matches with flexible indentation
func IndentationFlexibleReplacer(content, find string) []string {
	removeIndentation := func(text string) string {
		lines := strings.Split(text, "\n")
		minIndent := -1
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			if minIndent == -1 || indent < minIndent {
				minIndent = indent
			}
		}
		if minIndent <= 0 {
			return text
		}
		var result []string
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				result = append(result, line)
			} else if len(line) >= minIndent {
				result = append(result, line[minIndent:])
			} else {
				result = append(result, line)
			}
		}
		return strings.Join(result, "\n")
	}

	normalizedFind := removeIndentation(find)
	contentLines := strings.Split(content, "\n")
	findLines := strings.Split(find, "\n")
	var results []string

	for i := 0; i <= len(contentLines)-len(findLines); i++ {
		block := strings.Join(contentLines[i:i+len(findLines)], "\n")
		if removeIndentation(block) == normalizedFind {
			results = append(results, block)
		}
	}

	return results
}

// TrimmedBoundaryReplacer matches with trimmed boundaries
func TrimmedBoundaryReplacer(content, find string) []string {
	trimmedFind := strings.TrimSpace(find)
	if trimmedFind == find {
		return nil
	}

	var results []string
	if strings.Contains(content, trimmedFind) {
		results = append(results, trimmedFind)
	}

	lines := strings.Split(content, "\n")
	findLines := strings.Split(find, "\n")

	for i := 0; i <= len(lines)-len(findLines); i++ {
		block := strings.Join(lines[i:i+len(findLines)], "\n")
		if strings.TrimSpace(block) == trimmedFind {
			results = append(results, block)
		}
	}

	return results
}

// MultiOccurrenceReplacer yields all exact matches
func MultiOccurrenceReplacer(content, find string) []string {
	var results []string
	startIndex := 0
	for {
		index := strings.Index(content[startIndex:], find)
		if index == -1 {
			break
		}
		results = append(results, find)
		startIndex += index + len(find)
	}
	return results
}

// replace performs multi-strategy replacement matching opencode's behavior
func replace(content, oldString, newString string, replaceAll bool) (string, error) {
	if oldString == newString {
		return "", errors.New("oldString and newString must be different")
	}

	replacers := []Replacer{
		SimpleReplacer,
		LineTrimmedReplacer,
		BlockAnchorReplacer,
		WhitespaceNormalizedReplacer,
		IndentationFlexibleReplacer,
		TrimmedBoundaryReplacer,
		MultiOccurrenceReplacer,
	}

	notFound := true

	for _, replacer := range replacers {
		for _, search := range replacer(content, oldString) {
			index := strings.Index(content, search)
			if index == -1 {
				continue
			}
			notFound = false
			if replaceAll {
				return strings.ReplaceAll(content, search, newString), nil
			}
			lastIndex := strings.LastIndex(content, search)
			if index != lastIndex {
				continue // multiple matches, try next replacer
			}
			return content[:index] + newString + content[index+len(search):], nil
		}
	}

	if notFound {
		return "", errors.New("oldString not found in content")
	}
	return "", errors.New("Found multiple matches for oldString. Provide more surrounding lines in oldString to identify the correct match.")
}
