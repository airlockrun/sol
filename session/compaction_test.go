package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/testutil"
)

func TestSessionCompact_StreamErrorDoesNotMutateMessages(t *testing.T) {
	streamErr := errors.New("stream failed")
	tests := []struct {
		name   string
		events []stream.Event
	}{
		{
			name:   "immediate error",
			events: testutil.MockErrorResponse(streamErr),
		},
		{
			name: "error after partial delta",
			events: []stream.Event{
				{Type: stream.EventTextStart, Data: stream.TextStartEvent{}},
				{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "partial summary"}},
				{Type: stream.EventError, Data: stream.ErrorEvent{Error: streamErr}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New("test", "agent", "model", ModelLimits{})
			s.Messages = []Message{{Role: "user", Content: "keep this message"}}
			before := s.GetMessages()
			model := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
				StreamResponse: tt.events,
			})

			summary, err := s.Compact(context.Background(), model, []goai.Message{
				goai.NewUserMessage("summarize this"),
			}, nil)
			if !errors.Is(err, streamErr) {
				t.Fatalf("Compact error = %v, want %v", err, streamErr)
			}
			if summary != "" {
				t.Errorf("summary = %q, want empty", summary)
			}
			if got := s.GetMessages(); !reflect.DeepEqual(got, before) {
				t.Errorf("messages = %#v, want unchanged %#v", got, before)
			}
		})
	}
}

func TestSessionCompactAndContinue_MessageOrder(t *testing.T) {
	model := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{
		StreamResponse: testutil.MockTextResponse("internal summary", testutil.MockUsage(10, 3)),
	})
	s := New("test", "agent", "model", ModelLimits{})
	s.Messages = []Message{{Role: "assistant", Content: "state replaced by compaction"}}

	err := s.CompactAndContinue(context.Background(), model, []goai.Message{
		goai.NewSystemMessage("system prompt"),
		goai.NewUserMessage("initial request"),
		goai.NewAssistantMessage("work in progress"),
		goai.NewUserMessage("latest request"),
	}, nil)
	if err != nil {
		t.Fatalf("CompactAndContinue failed: %v", err)
	}

	want := []Message{
		{Role: "user", Content: "latest request"},
		{Role: "assistant", Content: "internal summary", Summary: true},
		{
			Role:    "user",
			Content: "The preceding assistant message is an internal compaction summary for context, not a user-visible assistant reply. Continue with the next steps if there are any, or ask the user for clarification if needed.",
		},
	}
	if got := s.GetMessages(); !reflect.DeepEqual(got, want) {
		t.Errorf("messages = %#v, want %#v", got, want)
	}
}

func TestPrune_ImagePartsStripped(t *testing.T) {
	// PruneProtect = 40_000, PruneMinimum = 20_000.
	// Images use a fixed ImageTokenEstimate=1500 per part, so we need
	// bulky tool outputs alongside the images to cross the thresholds.
	// Parts are iterated in reverse within each message — put image first so
	// the large tool output pushes total past PruneProtect before the image
	// is evaluated.
	largeOutput := strings.Repeat("A", 200_000) // ~50K tokens

	s := New("test", "agent", "model", ModelLimits{Context: 100000, Output: 4000})

	// Turn 1 (old): tool result with large tool output + image ref
	s.Messages = []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Parts: []Part{
			{Type: "tool", Tool: &ToolPart{CallID: "c1", Name: "run_js", Input: `{"code":"attachToContext()"}`, Status: "completed"}},
		}},
		{Role: "tool", Parts: []Part{
			{Type: "file", File: &FilePart{Data: "s3ref:tmp/img1.jpg", MimeType: "image/jpeg"}},
			{Type: "tool", Tool: &ToolPart{CallID: "c1", Name: "run_js", Output: largeOutput, Status: "completed"}},
		}},
		// Turn 2 (old): another tool result with image
		{Role: "user", Content: "second"},
		{Role: "assistant", Parts: []Part{
			{Type: "tool", Tool: &ToolPart{CallID: "c2", Name: "run_js", Input: `{}`, Status: "completed"}},
		}},
		{Role: "tool", Parts: []Part{
			{Type: "file", File: &FilePart{Data: "s3ref:tmp/img2.png", MimeType: "image/png"}},
			{Type: "tool", Tool: &ToolPart{CallID: "c2", Name: "run_js", Output: largeOutput, Status: "completed"}},
		}},
		// Turn 3 (recent — protected)
		{Role: "user", Content: "third"},
		{Role: "assistant", Content: "response"},
		// Turn 4 (recent — protected)
		{Role: "user", Content: "fourth"},
		{Role: "assistant", Content: "response2"},
	}

	pruned := s.Prune()
	if pruned == 0 {
		t.Fatal("expected pruning to happen")
	}

	// Check that old image parts were compacted.
	for _, msg := range s.Messages[:6] { // first 2 turns
		for _, p := range msg.Parts {
			if p.Type == "image" && !p.Compacted {
				t.Error("old image part should be compacted")
			}
		}
	}
}

func TestPrune_RecentImagesPreserved(t *testing.T) {
	largeOutput := strings.Repeat("A", 200_000)

	s := New("test", "agent", "model", ModelLimits{Context: 100000, Output: 4000})

	// Only 1 turn — should be protected (skip first 2 turns).
	s.Messages = []Message{
		{Role: "user", Content: "only turn"},
		{Role: "tool", Parts: []Part{
			{Type: "file", File: &FilePart{Data: "s3ref:tmp/img.jpg", MimeType: "image/jpeg"}},
			{Type: "tool", Tool: &ToolPart{CallID: "c1", Name: "run_js", Output: largeOutput, Status: "completed"}},
		}},
	}

	pruned := s.Prune()
	if pruned != 0 {
		t.Errorf("expected 0 pruned (recent turn protected), got %d", pruned)
	}

	// Image should NOT be compacted.
	for _, msg := range s.Messages {
		for _, p := range msg.Parts {
			if p.Type == "image" && p.Compacted {
				t.Error("recent image should not be compacted")
			}
		}
	}
}

func TestPrune_Disabled(t *testing.T) {
	s := New("test", "agent", "model", ModelLimits{Context: 100000, Output: 4000})
	s.CompactionConfig.Prune = false

	s.Messages = []Message{
		{Role: "user", Content: "old"},
		{Role: "user", Content: "old2"},
		{Role: "user", Content: "recent"},
		{Role: "tool", Parts: []Part{
			{Type: "tool", Tool: &ToolPart{CallID: "c1", Name: "run_js", Output: strings.Repeat("x", 300_000), Status: "completed"}},
		}},
	}

	pruned := s.Prune()
	if pruned != 0 {
		t.Errorf("pruning should be disabled, got %d", pruned)
	}
}
