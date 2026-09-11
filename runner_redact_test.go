package sol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/airlockrun/goai/message"
)

func TestRedactMessages_StructuredOutputAndNoMutation(t *testing.T) {
	content := message.ContentOutput{Value: []message.ToolContentItem{{Type: "text", Text: "secret"}}}
	msgs := []message.Message{
		message.NewToolMessage("a", "t", content),
		message.NewToolMessage("b", "t", message.JSONOutput{Value: json.RawMessage(`{"v":"secret","n":9007199254740993}`)}),
		message.NewToolMessage("c", "t", message.ErrorJSONOutput{Value: map[string]any{"v": "secret"}}),
		message.NewAssistantMessageWithParts(message.ToolCallPart{ID: "d", Name: "t", Input: json.RawMessage(`{"v":"secret"}`)}),
	}
	redacted := redactMessages(msgs, func(s string) string { return strings.ReplaceAll(s, "secret", `quote"replacement`) })
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || !strings.Contains(string(encoded), "9007199254740993") {
		t.Fatalf("redacted = %s", encoded)
	}
	if content.Value[0].Text != "secret" {
		t.Fatal("redactor mutated source content")
	}
	if got := string(msgs[3].Content.Parts[0].(message.ToolCallPart).Input); got != `{"v":"secret"}` {
		t.Fatal("redactor mutated source args")
	}
}
