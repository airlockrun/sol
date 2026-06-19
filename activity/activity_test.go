package activity

import (
	"encoding/json"
	"testing"
)

func TestSummarize(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		title     string
		meta      map[string]any
		output    string
		outcome   string
		wantKind  string
		wantLabel string
		wantLog   string
	}{
		{
			name: "read", tool: "read", title: "main.go",
			meta:     map[string]any{"lines": 120},
			wantKind: KindRead, wantLabel: "Read File: main.go", wantLog: "[read] main.go (120 lines)",
		},
		{
			name: "read one line", tool: "read", title: "x.go",
			meta:    map[string]any{"lines": 1},
			wantLog: "[read] x.go (1 line)",
		},
		{
			name: "write created", tool: "write", title: "new.go",
			meta:     map[string]any{"created": true, "lines": 10},
			wantKind: KindWrite, wantLabel: "Created File: new.go", wantLog: "[write] new.go (10 lines)",
		},
		{
			name: "write updated", tool: "write", title: "old.go",
			meta:      map[string]any{"created": false, "lines": 5},
			wantLabel: "Wrote File: old.go", wantLog: "[write] old.go (5 lines)",
		},
		{
			name: "edit", tool: "edit", title: "main.go",
			meta:     map[string]any{"added": 40, "removed": 3},
			wantKind: KindEdit, wantLabel: "Edit File: main.go", wantLog: "[edit] main.go +40 -3",
		},
		{
			name: "grep", tool: "grep", title: "TODO",
			meta:     map[string]any{"matches": 7},
			wantKind: KindSearch, wantLabel: "Searching: TODO", wantLog: "[grep] TODO (7 matches)",
		},
		{
			name: "glob", tool: "glob", title: "src",
			meta:     map[string]any{"matches": 1},
			wantKind: KindSearch, wantLabel: "Finding files: src", wantLog: "[glob] src (1 match)",
		},
		{
			name: "bash", tool: "bash", title: "List files", output: "a.go\nb.go\n",
			wantKind: KindCommand, wantLabel: "Running: List files", wantLog: "[bash] List files → a.go",
		},
		{
			name: "todo", tool: "todowrite",
			meta: map[string]any{"todos": []map[string]any{
				{"status": "completed"}, {"status": "in_progress"}, {"status": "pending"},
			}},
			wantKind: KindTodo, wantLabel: "Updated tasks (1/3)", wantLog: "[todo] 1 of 3 done",
		},
		{
			name: "exit", tool: "exit", title: "exit:success",
			wantKind: KindExit, wantLabel: "Finished: success", wantLog: "[exit] success",
		},
		{
			name: "error", tool: "edit", output: "boom\ndetails", outcome: "error",
			wantKind: KindError, wantLabel: "edit failed", wantLog: "[error] edit: boom",
		},
		{
			name: "denied", tool: "bash", outcome: "denied",
			wantKind: KindDenied, wantLabel: "bash denied", wantLog: "[denied] bash",
		},
		{
			name: "unknown", tool: "mytool", title: "x",
			wantKind: KindTool, wantLabel: "mytool: x", wantLog: "[tool] mytool: x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Summarize(tt.tool, nil, tt.title, tt.meta, tt.output, tt.outcome)
			if tt.wantKind != "" && a.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", a.Kind, tt.wantKind)
			}
			if tt.wantLabel != "" && a.Label != tt.wantLabel {
				t.Errorf("Label = %q, want %q", a.Label, tt.wantLabel)
			}
			if got := a.LogLine(); got != tt.wantLog {
				t.Errorf("LogLine = %q, want %q", got, tt.wantLog)
			}
		})
	}
}

// TestSummarize_JSONRoundTrippedMetadata covers the toolserver path where
// metadata numbers arrive as float64 and todos as []any of map.
func TestSummarize_JSONRoundTrippedMetadata(t *testing.T) {
	raw := `{"lines": 42}`
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatal(err)
	}
	a := Summarize("read", nil, "f.go", meta, "", "")
	if got := a.LogLine(); got != "[read] f.go (42 lines)" {
		t.Errorf("LogLine = %q", got)
	}
}
