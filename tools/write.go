package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/toolutil"
)

// WriteInput is the input schema for the write tool
type WriteInput struct {
	Content  string `json:"content" description:"The content to write to the file"`
	FilePath string `json:"filePath" description:"The absolute path to the file to write (must be absolute, not relative)"`
}

// Write creates the write tool
func Write() tool.Tool {
	return tool.New("write").
		Description(`Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- If this is an existing file, you MUST use the Read tool first to read the file's contents. This tool will fail if you did not read the file first.
- ALWAYS prefer editing existing files in the codebase. NEVER write new files unless explicitly required.
- NEVER proactively create documentation files (*.md) or README files. Only create documentation files if explicitly requested by the User.
- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked.
`).
		SchemaFromStruct(WriteInput{}).
		Execute(func(ctx context.Context, input json.RawMessage, opts tool.CallOptions) (tool.Result, error) {
			var args WriteInput
			if err := json.Unmarshal(input, &args); err != nil {
				return tool.Result{}, err
			}

			// Get working directory and session ID from context
			workDir, _ := ctx.Value(WorkDirKey).(string)
			if workDir == "" {
				workDir, _ = os.Getwd()
			}
			sessionID, _ := ctx.Value(SessionIDKey).(string)

			// Convert relative path to absolute (like opencode)
			filePath := args.FilePath
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(workDir, filePath)
			}

			// Check if file exists and read old content for diff
			exists := false
			var oldContent string
			if data, err := os.ReadFile(filePath); err == nil {
				exists = true
				oldContent = string(data)
				// For existing files, verify it hasn't been modified since last read
				if sessionID != "" {
					if err := toolutil.FileTime.Assert(sessionID, filePath); err != nil {
						return tool.Result{
							Output: fmt.Sprintf("Error: %v", err),
							Title:  fmt.Sprintf("write: %s (error)", filepath.Base(filePath)),
						}, nil
					}
				}
			}

			// Generate diff and request permission
			diff := toolutil.GenerateDiff(filePath, oldContent, args.Content)
			relPath, _ := filepath.Rel(workDir, filePath)
			if relPath == "" {
				relPath = filePath
			}

			err := AskPermission(ctx, PermissionRequest{
				SessionID:  sessionID,
				Permission: "edit",
				Patterns:   []string{relPath},
				Always:     []string{"*"},
				Metadata: map[string]any{
					"filepath": filePath,
					"diff":     toolutil.TrimDiff(diff, 50),
				},
			})
			if err != nil {
				return tool.Result{
					Output: fmt.Sprintf("Error: %v", err),
					Title:  fmt.Sprintf("write: %s (rejected)", filepath.Base(filePath)),
				}, nil
			}

			// Ensure directory exists
			dir := filepath.Dir(filePath)
			if dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0755); err != nil {
					return tool.Result{
						Output: fmt.Sprintf("Error creating directory: %v", err),
						Title:  fmt.Sprintf("write: %s (error)", filepath.Base(filePath)),
					}, nil
				}
			}

			// Write the file
			if err := os.WriteFile(filePath, []byte(args.Content), 0644); err != nil {
				return tool.Result{
					Output: fmt.Sprintf("Error writing file: %v", err),
					Title:  fmt.Sprintf("write: %s (error)", filepath.Base(filePath)),
				}, nil
			}

			// Update file time tracking after successful write
			if sessionID != "" {
				toolutil.FileTime.Read(sessionID, filePath)
			}

			// Calculate relative path for title (like opencode)
			// relPath already calculated above for permission request

			return tool.Result{
				Output: "Wrote file successfully.",
				Title:  relPath,
				Metadata: map[string]any{
					"filepath": filePath,
					"exists":   exists,
				},
			}, nil
		}).
		Build()
}
