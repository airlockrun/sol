package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/goai/testutil"
)

func TestIsOverflow_UnknownBudget(t *testing.T) {
	s := New("test", "agent", "model", ModelLimits{})
	s.Tokens.Input = 100_000
	if !s.CompactionConfig.Auto {
		t.Fatal("automatic compaction must remain enabled")
	}
	if s.IsOverflow() {
		t.Fatal("unknown budget must not imply overflow")
	}
	if err := s.ModelLimits.Validate(true); err == nil {
		t.Fatal("explicit validation must reject an unknown automatic budget")
	}
}

func TestIsOverflow_BoundedOutputReserve(t *testing.T) {
	for _, tt := range []struct {
		name   string
		limits ModelLimits
		budget int
	}{
		{"equal maxima", ModelLimits{Context: 8192, Output: 8192}, 4096},
		{"larger output", ModelLimits{Context: 8192, Output: 32768}, 4096},
		{"unknown output", ModelLimits{Context: 8192}, 4096},
		{"smaller output", ModelLimits{Context: 8192, Output: 2048}, 6144},
		{"default cap", ModelLimits{Context: 100000, Output: 50000}, 68000},
		{"default reserve", ModelLimits{Context: 100000}, 68000},
		{"stricter input", ModelLimits{Context: 8192, Input: 3000, Output: 8192}, 3000},
		{"input equals context", ModelLimits{Context: 8192, Input: 8192, Output: 8192}, 4096},
		{"input only", ModelLimits{Input: 8192, Output: 8192}, 8192},
		{"minimal context", ModelLimits{Context: 1, Output: 1}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.limits.Validate(true); err != nil {
				t.Fatal(err)
			}
			s := New("test", "agent", "model", tt.limits)
			t.Cleanup(s.Cancel)
			for _, used := range []int{0, tt.budget, tt.budget + 1} {
				s.Tokens = Tokens{Input: used}
				if got := s.IsOverflow(); got != (used > tt.budget) {
					t.Errorf("IsOverflow at %d tokens = %v, budget %d", used, got, tt.budget)
				}
			}
			s.Tokens = Tokens{Input: tt.budget - 1, Output: 1}
			s.Tokens.Cache.Read = 1
			if !s.IsOverflow() {
				t.Error("overflow must include output and cached input")
			}
			if s.ModelLimits != tt.limits {
				t.Fatalf("model maxima changed: %+v", s.ModelLimits)
			}
		})
	}
}

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

func TestPrune_UpdatesEstimateAndReplacement(t *testing.T) {
	s := New("test", "agent", "model", ModelLimits{Input: 80_000})
	s.CompactionConfig.PrunedMessage = func(info PrunedInfo) string { return "removed " + info.Type }
	s.Messages = FromGoAIMessages([]goai.Message{
		goai.NewUserMessage("old"),
		goai.NewToolMessage("c", "read", message.ContentOutput{Value: []message.ToolContentItem{
			{Type: "text", Text: strings.Repeat("x", 400_000)},
			{Type: "image-data", Data: "secret image", MediaType: "image/png"},
		}}),
		goai.NewUserMessage("recent"), goai.NewUserMessage("latest"),
	})
	s.Tokens.Input = EstimateMessagesTokens(s.Messages) + 100
	if !s.IsOverflow() {
		t.Fatal("expected overflow")
	}
	if got := s.Prune(); got != 1 {
		t.Fatalf("Prune = %d", got)
	}
	if s.IsOverflow() {
		t.Fatalf("still overflowing: %+v", s.Tokens)
	}
	if s.Tokens.Input != EstimateMessagesTokens(s.Messages)+100 {
		t.Fatalf("token estimate = %d, want transcript estimate plus original overhead", s.Tokens.Input)
	}
	output := s.ToGoAIMessages()[1].Content.Parts[0].(message.ToolResultPart).Output
	if message.ToolOutputText(output) != "removed tool_output" {
		t.Fatalf("output = %#v", output)
	}
	if strings.Contains(s.Messages[1].Parts[0].Tool.Output, "secret") {
		t.Fatal("pruned payload retained")
	}
}

func TestCompactAndContinue_PreservesMultipartUser(t *testing.T) {
	model := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{StreamResponse: testutil.MockTextResponse("summary", testutil.MockUsage(10, 2))})
	s := New("test", "agent", "model", ModelLimits{})
	user := message.NewUserMessageWithParts(goai.TextPart{Text: "look"}, message.FilePart{Data: message.FileDataText{Text: "file text"}, MimeType: "text/plain"})
	if err := s.CompactAndContinue(context.Background(), model, []goai.Message{user}, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.ToGoAIMessages()[0]; !reflect.DeepEqual(got, user) {
		t.Fatalf("user = %#v", got)
	}
	if s.Tokens.Input != EstimateMessagesTokens(s.Messages) {
		t.Fatal("summary tokens not reset")
	}
}

func TestCompact_RejectsIncompleteSummary(t *testing.T) {
	for _, finish := range []stream.FinishReason{stream.FinishReasonLength, stream.FinishReasonError, stream.FinishReasonToolCalls} {
		t.Run(string(finish), func(t *testing.T) {
			model := testutil.NewMockLanguageModel(testutil.MockLanguageModelOptions{StreamResponse: []stream.Event{
				{Type: stream.EventTextDelta, Data: stream.TextDeltaEvent{Text: "partial"}},
				{Type: stream.EventFinish, Data: stream.FinishEvent{FinishReason: finish}},
			}})
			s := New("test", "agent", "model", ModelLimits{})
			s.Messages = []Message{{Role: "user", Content: "keep"}}
			if err := s.CompactAndContinue(context.Background(), model, s.ToGoAIMessages(), nil); err == nil {
				t.Fatal("expected error")
			}
			if len(s.Messages) != 1 || s.Messages[0].Content != "keep" {
				t.Fatal("history mutated")
			}
		})
	}
}
