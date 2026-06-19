// Package activity turns a tool execution result into a compact, presentation
// -ready summary: a short structured Action (for a live-actions feed) and a
// one-line log string (for a curated codegen log). It exists so a consumer —
// airlock's build log, a Sol embedder's UI — can show "what the agent did"
// without echoing the full model-facing output (file contents, edited line
// bodies, full command output).
//
// It deliberately takes primitives (tool name, input, title, metadata, output,
// outcome) rather than goai/sol types, so it has no dependency on the event or
// message packages and can be reused anywhere.
package activity

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Kinds classify an Action for rendering. Stable strings — consumers may
// switch on them.
const (
	KindCommand = "command"
	KindSearch  = "search"
	KindRead    = "read"
	KindWrite   = "write"
	KindEdit    = "edit"
	KindTodo    = "todo"
	KindExit    = "exit"
	KindError   = "error"
	KindDenied  = "denied"
	KindTool    = "tool"
)

// Action is a compact, structured description of one tool call.
type Action struct {
	Kind       string `json:"kind"`
	Label      string `json:"label"`            // headline, e.g. "Read File: main.go"
	Detail     string `json:"detail,omitempty"` // secondary, e.g. "120 lines"
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
}

const detailMax = 200

// Summarize maps a tool result to a structured Action. outcome is "" for a
// normal result, "error" for a failed one, or "denied" for a refused one
// (mirroring message.ToolOutcome). title and metadata are the tool's
// Result.Title / Result.Metadata; output is the model-facing text.
func Summarize(toolName string, input json.RawMessage, title string, metadata map[string]any, output, outcome string) Action {
	a := Action{ToolName: toolName}

	switch outcome {
	case "error":
		a.Kind = KindError
		a.Label = toolName + " failed"
		a.Detail = firstLine(output, detailMax)
		return a
	case "denied":
		a.Kind = KindDenied
		a.Label = toolName + " denied"
		a.Detail = firstLine(output, detailMax)
		return a
	}

	switch toolName {
	case "bash":
		a.Kind = KindCommand
		if title != "" {
			a.Label = "Running: " + title
		} else {
			a.Label = "Running command"
		}
		a.Detail = firstLine(output, detailMax)
	case "grep":
		a.Kind = KindSearch
		a.Label = "Searching: " + title
		a.Detail = countDetail(metadata, "matches", "match", "matches")
	case "glob":
		a.Kind = KindSearch
		a.Label = "Finding files: " + title
		a.Detail = countDetail(metadata, "matches", "match", "matches")
	case "read":
		a.Kind = KindRead
		a.Label = "Read File: " + title
		a.Detail = countDetail(metadata, "lines", "line", "lines")
	case "write":
		a.Kind = KindWrite
		if b, _ := metadata["created"].(bool); b {
			a.Label = "Created File: " + title
		} else {
			a.Label = "Wrote File: " + title
		}
		a.Detail = countDetail(metadata, "lines", "line", "lines")
	case "edit":
		a.Kind = KindEdit
		a.Label = "Edit File: " + title
		a.Detail = fmt.Sprintf("+%d -%d", numFromMeta(metadata, "added"), numFromMeta(metadata, "removed"))
	case "todowrite", "todoread":
		a.Kind = KindTodo
		done, total := TodoCounts(metadata)
		a.Label = fmt.Sprintf("Updated tasks (%d/%d)", done, total)
		a.Detail = fmt.Sprintf("%d of %d done", done, total)
	case "exit":
		a.Kind = KindExit
		a.Label = "Finished: " + strings.TrimPrefix(title, "exit:")
	default:
		a.Kind = KindTool
		if title != "" {
			a.Label = toolName + ": " + title
		} else {
			a.Label = toolName
		}
	}
	return a
}

// LogLine renders the Action as a single codegen-log line.
func (a Action) LogLine() string {
	switch a.Kind {
	case KindCommand:
		if a.Detail != "" {
			return fmt.Sprintf("[bash] %s → %s", strings.TrimPrefix(a.Label, "Running: "), a.Detail)
		}
		return "[bash] " + strings.TrimPrefix(a.Label, "Running: ")
	case KindSearch:
		return fmt.Sprintf("[%s] %s (%s)", searchPrefix(a.ToolName), trimLabel(a.Label), a.Detail)
	case KindRead:
		return fmt.Sprintf("[read] %s (%s)", trimLabel(a.Label), a.Detail)
	case KindWrite:
		return fmt.Sprintf("[write] %s (%s)", trimLabel(a.Label), a.Detail)
	case KindEdit:
		return fmt.Sprintf("[edit] %s %s", trimLabel(a.Label), a.Detail)
	case KindTodo:
		return "[todo] " + a.Detail
	case KindExit:
		return "[exit] " + strings.TrimPrefix(a.Label, "Finished: ")
	case KindError:
		return fmt.Sprintf("[error] %s: %s", a.ToolName, a.Detail)
	case KindDenied:
		return fmt.Sprintf("[denied] %s", a.ToolName)
	default:
		return "[tool] " + a.Label
	}
}

// trimLabel strips the "Verb: " prefix from a label to recover the bare target
// (file/pattern) for the compact log line.
func trimLabel(label string) string {
	if i := strings.Index(label, ": "); i >= 0 {
		return label[i+2:]
	}
	return label
}

func searchPrefix(toolName string) string {
	if toolName == "glob" {
		return "glob"
	}
	return "grep"
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// countDetail renders "<n> <singular|plural>" from a numeric metadata key.
func countDetail(metadata map[string]any, key, singular, plural string) string {
	n := numFromMeta(metadata, key)
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// numFromMeta reads an integer from metadata, tolerating the float64 that
// results when the metadata round-trips through JSON (the toolserver path).
func numFromMeta(metadata map[string]any, key string) int {
	switch v := metadata[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

// TodoCounts returns (completed, total) from a metadata["todos"] list,
// tolerating both the in-process []TodoItem-as-[]any and the JSON-round-tripped
// []any of map[string]any.
func TodoCounts(metadata map[string]any) (done, total int) {
	raw, ok := metadata["todos"]
	if !ok {
		return 0, 0
	}
	// Re-marshal then unmarshal into a minimal shape — robust to whatever
	// concrete type carried the todos.
	b, err := json.Marshal(raw)
	if err != nil {
		return 0, 0
	}
	var items []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(b, &items); err != nil {
		return 0, 0
	}
	for _, it := range items {
		total++
		if it.Status == "completed" {
			done++
		}
	}
	return done, total
}
