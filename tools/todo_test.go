package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/airlockrun/goai/tool"
)

func TestTodoStorage(t *testing.T) {
	// Use temp directory for test
	tmpDir := t.TempDir()
	originalXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", originalXDG)

	// Create a fresh storage for test
	storage := &TodoStorage{
		cache: make(map[string][]TodoItem),
	}

	sessionID := "test-session-123"

	// Test Get with no existing todos
	todos, err := storage.Get(sessionID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(todos) != 0 {
		t.Errorf("expected empty todos, got %d", len(todos))
	}

	// Test Update
	testTodos := []TodoItem{
		{ID: "1", Content: "First task", Status: "pending", Priority: "high"},
		{ID: "2", Content: "Second task", Status: "in_progress", Priority: "medium"},
	}
	err = storage.Update(sessionID, testTodos)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Test Get returns updated todos
	todos, err = storage.Get(sessionID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(todos) != 2 {
		t.Errorf("expected 2 todos, got %d", len(todos))
	}
	if todos[0].Content != "First task" {
		t.Errorf("expected 'First task', got %q", todos[0].Content)
	}

	// Verify file was written
	expectedPath := filepath.Join(tmpDir, "sol", "todo", sessionID+".json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("expected file to exist at %s", expectedPath)
	}

	// Clear cache and verify persistence
	storage.cache = make(map[string][]TodoItem)
	todos, err = storage.Get(sessionID)
	if err != nil {
		t.Fatalf("Get after cache clear failed: %v", err)
	}
	if len(todos) != 2 {
		t.Errorf("expected 2 todos after cache clear, got %d", len(todos))
	}
}

func TestCountNonCompleted(t *testing.T) {
	tests := []struct {
		name     string
		todos    []TodoItem
		expected int
	}{
		{
			name:     "empty",
			todos:    []TodoItem{},
			expected: 0,
		},
		{
			name: "all pending",
			todos: []TodoItem{
				{Status: "pending"},
				{Status: "pending"},
			},
			expected: 2,
		},
		{
			name: "mixed",
			todos: []TodoItem{
				{Status: "pending"},
				{Status: "completed"},
				{Status: "in_progress"},
				{Status: "completed"},
			},
			expected: 2,
		},
		{
			name: "all completed",
			todos: []TodoItem{
				{Status: "completed"},
				{Status: "completed"},
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countNonCompleted(tt.todos)
			if got != tt.expected {
				t.Errorf("countNonCompleted() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestTodoWriteTool(t *testing.T) {
	// Use temp directory for test
	tmpDir := t.TempDir()
	originalXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", originalXDG)

	// Reset storage for test
	todoStorage = &TodoStorage{
		cache: make(map[string][]TodoItem),
	}

	tw := TodoWrite()

	// Create context with session ID
	ctx := context.WithValue(context.Background(), SessionIDKey, "test-write-session")

	// Execute tool
	input := TodoWriteInput{
		Todos: []TodoItem{
			{ID: "1", Content: "Test task", Status: "pending", Priority: "high"},
			{ID: "2", Content: "Another task", Status: "completed", Priority: "low"},
		},
	}
	inputJSON, _ := json.Marshal(input)

	result, err := tw.Execute(ctx, inputJSON, tool.CallOptions{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Check title shows non-completed count
	if result.Title != "1 todos" {
		t.Errorf("expected title '1 todos', got %q", result.Title)
	}

	// Check output is JSON
	var outputTodos []TodoItem
	if err := json.Unmarshal([]byte(result.Output), &outputTodos); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if len(outputTodos) != 2 {
		t.Errorf("expected 2 todos in output, got %d", len(outputTodos))
	}

	// Check metadata
	if result.Metadata == nil {
		t.Error("expected metadata, got nil")
	}
}

func TestTodoReadTool(t *testing.T) {
	// Use temp directory for test
	tmpDir := t.TempDir()
	originalXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", originalXDG)

	// Reset storage for test
	todoStorage = &TodoStorage{
		cache: make(map[string][]TodoItem),
	}

	sessionID := "test-read-session"

	// Pre-populate some todos
	testTodos := []TodoItem{
		{ID: "1", Content: "Task 1", Status: "in_progress", Priority: "high"},
		{ID: "2", Content: "Task 2", Status: "pending", Priority: "medium"},
		{ID: "3", Content: "Task 3", Status: "completed", Priority: "low"},
	}
	_ = todoStorage.Update(sessionID, testTodos)

	tr := TodoRead()

	// Create context with session ID
	ctx := context.WithValue(context.Background(), SessionIDKey, sessionID)

	// Execute tool
	result, err := tr.Execute(ctx, []byte("{}"), tool.CallOptions{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Check title shows non-completed count (2: in_progress + pending)
	if result.Title != "2 todos" {
		t.Errorf("expected title '2 todos', got %q", result.Title)
	}

	// Check output is JSON with all todos
	var outputTodos []TodoItem
	if err := json.Unmarshal([]byte(result.Output), &outputTodos); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if len(outputTodos) != 3 {
		t.Errorf("expected 3 todos in output, got %d", len(outputTodos))
	}
}

func TestTodoReadTool_EmptySession(t *testing.T) {
	// Use temp directory for test
	tmpDir := t.TempDir()
	originalXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", originalXDG)

	// Reset storage for test
	todoStorage = &TodoStorage{
		cache: make(map[string][]TodoItem),
	}

	tr := TodoRead()

	// Create context with new session ID (no existing todos)
	ctx := context.WithValue(context.Background(), SessionIDKey, "empty-session")

	// Execute tool
	result, err := tr.Execute(ctx, []byte("{}"), tool.CallOptions{})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Check title shows 0 todos
	if result.Title != "0 todos" {
		t.Errorf("expected title '0 todos', got %q", result.Title)
	}

	// Check output is empty array
	var outputTodos []TodoItem
	if err := json.Unmarshal([]byte(result.Output), &outputTodos); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if len(outputTodos) != 0 {
		t.Errorf("expected 0 todos in output, got %d", len(outputTodos))
	}
}
