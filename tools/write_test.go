package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/airlockrun/goai/tool"
	"github.com/airlockrun/sol/bus"
)

func setupWriteTestHandler(t *testing.T) (context.Context, func()) {
	t.Helper()
	b := bus.New()
	pm := bus.NewPermissionManager(b)
	pm.SetRules([]bus.PermissionRule{
		{Permission: "*", Pattern: "*", Action: "allow"},
	})
	ctx := bus.WithPermissionManager(context.Background(), pm)
	return ctx, func() {}
}

func TestWriteTool_NewFile(t *testing.T) {
	baseCtx, cleanup := setupWriteTestHandler(t)
	defer cleanup()

	tmpDir := t.TempDir()
	writeTool := Write()

	// Create context with working directory
	ctx := context.WithValue(baseCtx, WorkDirKey, tmpDir)

	// Write a new file
	filePath := filepath.Join(tmpDir, "test.txt")
	input := WriteInput{
		FilePath: filePath,
		Content:  "Hello, World!",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := writeTool.Execute(ctx, inputJSON, tool.CallOptions{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Check output matches opencode format
	if result.Output != "Wrote file successfully." {
		t.Errorf("expected output 'Wrote file successfully.', got %q", result.Output)
	}

	// Check title is relative path
	if result.Title != "test.txt" {
		t.Errorf("expected title 'test.txt', got %q", result.Title)
	}

	// Check metadata
	if result.Metadata["filepath"] != filePath {
		t.Errorf("expected filepath %q in metadata, got %v", filePath, result.Metadata["filepath"])
	}
	if result.Metadata["exists"] != false {
		t.Errorf("expected exists=false in metadata, got %v", result.Metadata["exists"])
	}

	// Verify file was written
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != "Hello, World!" {
		t.Errorf("expected content 'Hello, World!', got %q", string(content))
	}
}

func TestWriteTool_OverwriteExisting(t *testing.T) {
	baseCtx, cleanup := setupWriteTestHandler(t)
	defer cleanup()

	tmpDir := t.TempDir()

	// Create existing file
	filePath := filepath.Join(tmpDir, "existing.txt")
	if err := os.WriteFile(filePath, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	writeTool := Write()
	ctx := context.WithValue(baseCtx, WorkDirKey, tmpDir)

	input := WriteInput{
		FilePath: filePath,
		Content:  "new content",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := writeTool.Execute(ctx, inputJSON, tool.CallOptions{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Check metadata shows file existed
	if result.Metadata["exists"] != true {
		t.Errorf("expected exists=true in metadata, got %v", result.Metadata["exists"])
	}

	// Verify file was overwritten
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != "new content" {
		t.Errorf("expected content 'new content', got %q", string(content))
	}
}

func TestWriteTool_CreateDirectory(t *testing.T) {
	baseCtx, cleanup := setupWriteTestHandler(t)
	defer cleanup()

	tmpDir := t.TempDir()

	writeTool := Write()
	ctx := context.WithValue(baseCtx, WorkDirKey, tmpDir)

	// Write to nested path that doesn't exist
	filePath := filepath.Join(tmpDir, "nested", "dir", "file.txt")
	input := WriteInput{
		FilePath: filePath,
		Content:  "nested content",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := writeTool.Execute(ctx, inputJSON, tool.CallOptions{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Output != "Wrote file successfully." {
		t.Errorf("expected success message, got %q", result.Output)
	}

	// Check title shows relative path
	if result.Title != "nested/dir/file.txt" {
		t.Errorf("expected title 'nested/dir/file.txt', got %q", result.Title)
	}

	// Verify file was written
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != "nested content" {
		t.Errorf("expected content 'nested content', got %q", string(content))
	}
}

func TestWriteTool_RelativePath(t *testing.T) {
	baseCtx, cleanup := setupWriteTestHandler(t)
	defer cleanup()

	tmpDir := t.TempDir()

	writeTool := Write()
	ctx := context.WithValue(baseCtx, WorkDirKey, tmpDir)

	// Use relative path (should be converted to absolute)
	input := WriteInput{
		FilePath: "relative.txt",
		Content:  "relative content",
	}
	inputJSON, _ := json.Marshal(input)

	result, err := writeTool.Execute(ctx, inputJSON, tool.CallOptions{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Check metadata has absolute path
	expectedPath := filepath.Join(tmpDir, "relative.txt")
	if result.Metadata["filepath"] != expectedPath {
		t.Errorf("expected filepath %q, got %v", expectedPath, result.Metadata["filepath"])
	}

	// Verify file was written at correct location
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != "relative content" {
		t.Errorf("expected content 'relative content', got %q", string(content))
	}
}
