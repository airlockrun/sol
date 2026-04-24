package session

import (
	"strings"
	"testing"
)

func TestPrune_ImagePartsStripped(t *testing.T) {
	// PruneProtect = 40_000, PruneMinimum = 20_000.
	// Images are now a fixed ImageTokenEstimate=1500 per part, so we need
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
			{Type: "image", Image: &ImagePart{Image: "s3ref:tmp/img1.jpg", MimeType: "image/jpeg"}},
			{Type: "tool", Tool: &ToolPart{CallID: "c1", Name: "run_js", Output: largeOutput, Status: "completed"}},
		}},
		// Turn 2 (old): another tool result with image
		{Role: "user", Content: "second"},
		{Role: "assistant", Parts: []Part{
			{Type: "tool", Tool: &ToolPart{CallID: "c2", Name: "run_js", Input: `{}`, Status: "completed"}},
		}},
		{Role: "tool", Parts: []Part{
			{Type: "image", Image: &ImagePart{Image: "s3ref:tmp/img2.png", MimeType: "image/png"}},
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
			{Type: "image", Image: &ImagePart{Image: "s3ref:tmp/img.jpg", MimeType: "image/jpeg"}},
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
