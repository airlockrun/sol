package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/airlockrun/goai/message"
)

// TestRoundtrip_ToolOutcomePreserved guards the regression where the
// goai→session→goai round-trip flattened every tool result to a success
// TextOutput, so an errored/denied tool rendered with a green dot.
func TestRoundtrip_ToolOutcomePreserved(t *testing.T) {
	cases := []struct {
		name        string
		out         message.ToolResultOutput
		wantOutcome string
		wantType    string // concrete type after the round-trip
	}{
		{"success", message.TextOutput{Value: "ok"}, "success", "text"},
		{"error", message.ErrorTextOutput{Value: "boom"}, "error", "error-text"},
		{"denied", message.ExecutionDeniedOutput{Reason: "no"}, "denied", "execution-denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goaiMsg := message.NewToolMessage("call1", "run_js", tc.out)

			sess := FromGoAIMessage(goaiMsg)
			tp := sess.Parts[0].Tool
			if tp == nil {
				t.Fatalf("no tool part")
			}
			if tp.Outcome != tc.wantOutcome {
				t.Fatalf("session ToolPart.Outcome = %q, want %q", tp.Outcome, tc.wantOutcome)
			}

			back := MessageToGoAI(sess)
			if len(back) != 1 {
				t.Fatalf("MessageToGoAI returned %d messages", len(back))
			}
			trp, ok := back[0].Content.Parts[0].(message.ToolResultPart)
			if !ok {
				t.Fatalf("part is %T, want ToolResultPart", back[0].Content.Parts[0])
			}
			if got := message.ToolOutcome(trp.Output); got != tc.wantOutcome {
				t.Fatalf("round-trip ToolOutcome = %q, want %q", got, tc.wantOutcome)
			}
			b, _ := message.MarshalOutput(trp.Output)
			if !json.Valid(b) || !strings.Contains(string(b), `"type":"`+tc.wantType+`"`) {
				t.Fatalf("round-trip output = %s, want type %q", b, tc.wantType)
			}
		})
	}
}

func TestFromGoAIMessage_TextOnly(t *testing.T) {
	goaiMsg := message.NewUserMessage("hello")
	sm := FromGoAIMessage(goaiMsg)

	if sm.Role != "user" {
		t.Errorf("Role = %q, want user", sm.Role)
	}
	if sm.Content != "hello" {
		t.Errorf("Content = %q, want hello", sm.Content)
	}
	if len(sm.Parts) != 0 {
		t.Errorf("Parts len = %d, want 0", len(sm.Parts))
	}
}

func TestFromGoAIMessage_AssistantWithToolCall(t *testing.T) {
	goaiMsg := message.NewAssistantMessageWithParts(
		message.TextPart{Text: "thinking..."},
		message.ToolCallPart{
			ID:    "call_1",
			Name:  "run_js",
			Input: json.RawMessage(`{"code":"1+1"}`),
		},
	)
	sm := FromGoAIMessage(goaiMsg)

	if sm.Role != "assistant" {
		t.Fatalf("Role = %q, want assistant", sm.Role)
	}
	if len(sm.Parts) != 2 {
		t.Fatalf("Parts len = %d, want 2", len(sm.Parts))
	}
	if sm.Parts[0].Type != "text" || sm.Parts[0].Text != "thinking..." {
		t.Errorf("Parts[0] = %+v, want text part", sm.Parts[0])
	}
	if sm.Parts[1].Type != "tool" || sm.Parts[1].Tool == nil {
		t.Fatalf("Parts[1] = %+v, want tool part", sm.Parts[1])
	}
	if sm.Parts[1].Tool.CallID != "call_1" || sm.Parts[1].Tool.Name != "run_js" {
		t.Errorf("Tool = %+v", sm.Parts[1].Tool)
	}
}

func TestFromGoAIMessage_ToolResultWithImage(t *testing.T) {
	goaiMsg := message.Message{
		Role: "tool",
		Content: message.Content{Parts: []message.Part{
			message.ToolResultPart{
				ToolCallID: "call_1",
				ToolName:   "run_js",
				Output:     message.TextOutput{Value: "ok"},
			},
			message.ImagePart{
				Image:    "base64data",
				MimeType: "image/jpeg",
			},
		}},
	}
	sm := FromGoAIMessage(goaiMsg)

	if sm.Role != "tool" {
		t.Fatalf("Role = %q, want tool", sm.Role)
	}
	if len(sm.Parts) != 2 {
		t.Fatalf("Parts len = %d, want 2", len(sm.Parts))
	}
	if sm.Parts[0].Type != "tool" || sm.Parts[0].Tool.Output != "ok" {
		t.Errorf("Parts[0] = %+v", sm.Parts[0])
	}
	if sm.Parts[1].Type != "image" || sm.Parts[1].Image.Image != "base64data" {
		t.Errorf("Parts[1] = %+v", sm.Parts[1])
	}
}

func TestRoundtrip_TextMessage(t *testing.T) {
	original := message.NewUserMessage("hello world")
	sm := FromGoAIMessage(original)
	result := MessagesToGoAI([]Message{sm})

	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if result[0].Content.Text != "hello world" {
		t.Errorf("Text = %q, want hello world", result[0].Content.Text)
	}
}

func TestRoundtrip_AssistantWithParts(t *testing.T) {
	original := message.NewAssistantMessageWithParts(
		message.TextPart{Text: "here is the result"},
		message.ToolCallPart{
			ID:    "call_abc",
			Name:  "bash",
			Input: json.RawMessage(`{"cmd":"ls"}`),
		},
	)

	sm := FromGoAIMessage(original)
	result := MessagesToGoAI([]Message{sm})

	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if !result[0].Content.IsMultiPart() {
		t.Fatal("expected multipart")
	}
	if len(result[0].Content.Parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(result[0].Content.Parts))
	}
	tp, ok := result[0].Content.Parts[0].(message.TextPart)
	if !ok || tp.Text != "here is the result" {
		t.Errorf("TextPart = %+v", result[0].Content.Parts[0])
	}
	tc, ok := result[0].Content.Parts[1].(message.ToolCallPart)
	if !ok || tc.ID != "call_abc" || tc.Name != "bash" {
		t.Errorf("ToolCallPart = %+v", result[0].Content.Parts[1])
	}
}

func TestRoundtrip_ToolResultWithImage(t *testing.T) {
	original := message.Message{
		Role: "tool",
		Content: message.Content{Parts: []message.Part{
			message.ToolResultPart{
				ToolCallID: "call_1",
				ToolName:   "run_js",
				Output:     message.TextOutput{Value: "done"},
			},
			message.ImagePart{
				Image:    "imgdata",
				MimeType: "image/png",
			},
		}},
	}

	sm := FromGoAIMessage(original)
	result := MessagesToGoAI([]Message{sm})

	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if !result[0].Content.IsMultiPart() {
		t.Fatal("expected multipart")
	}
	// Should have ToolResultPart + ImagePart
	if len(result[0].Content.Parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(result[0].Content.Parts))
	}
	if _, ok := result[0].Content.Parts[0].(message.ToolResultPart); !ok {
		t.Errorf("expected ToolResultPart, got %T", result[0].Content.Parts[0])
	}
	img, ok := result[0].Content.Parts[1].(message.ImagePart)
	if !ok {
		t.Fatalf("expected ImagePart, got %T", result[0].Content.Parts[1])
	}
	if img.Image != "imgdata" || img.MimeType != "image/png" {
		t.Errorf("ImagePart = %+v", img)
	}
}

func TestRoundtrip_CompactedImageStripped(t *testing.T) {
	sm := Message{
		Role: "tool",
		Parts: []Part{
			{Type: "tool", Tool: &ToolPart{CallID: "c1", Name: "run_js", Output: "ok", Status: "completed"}},
			{Type: "image", Image: &ImagePart{Image: "data", MimeType: "image/jpeg"}, Compacted: true},
		},
	}

	result := MessagesToGoAI([]Message{sm})
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	// Compacted image should be stripped — tool result only, no attachments.
	if result[0].Content.IsMultiPart() {
		for _, p := range result[0].Content.Parts {
			if _, ok := p.(message.ImagePart); ok {
				t.Error("compacted image should have been stripped")
			}
		}
	}
}

func TestMessageToGoAI_FilePart(t *testing.T) {
	sm := Message{
		Role: "tool",
		Parts: []Part{
			{Type: "tool", Tool: &ToolPart{CallID: "c1", Name: "run_js", Output: "ok", Status: "completed"}},
			{Type: "file", File: &FilePart{Data: "pdf-data", MimeType: "application/pdf", Filename: "report.pdf"}},
		},
	}

	result := MessageToGoAI(sm)
	if len(result) != 1 {
		t.Fatalf("len = %d, want 1", len(result))
	}
	if len(result[0].Content.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(result[0].Content.Parts))
	}
	fp, ok := result[0].Content.Parts[1].(message.FilePart)
	if !ok {
		t.Fatalf("expected FilePart, got %T", result[0].Content.Parts[1])
	}
	if fp.Filename != "report.pdf" {
		t.Errorf("Filename = %q", fp.Filename)
	}
}
