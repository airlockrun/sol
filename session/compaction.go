package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol/bus"
)

const (
	// OutputTokenMax is the maximum output tokens we expect from the model.
	// Matches opencode's SessionPrompt.OUTPUT_TOKEN_MAX.
	OutputTokenMax = 32000

	// PruneMinimum is the minimum tokens to prune (to avoid tiny prunes).
	PruneMinimum = 20_000

	// PruneProtect is the number of tokens worth of tool calls to protect.
	PruneProtect = 40_000

	// CharsPerToken is the estimated number of characters per token.
	// Used for estimating token counts when actual counts aren't available.
	CharsPerToken = 4

	// ImageTokenEstimate is the per-image token budget used during pruning.
	// Mid-of-road: OpenAI charges ~85-170 per tile, Anthropic ~1000/image.
	// Providers bill real image tokens regardless of whether we ship base64
	// or a URL — so we can't count the transport string.
	ImageTokenEstimate = 1500

	// FileTokenEstimate is the per-file token budget used during pruning.
	// Heuristic; non-image files are typically PDFs where token cost tracks
	// page count more than bytes.
	FileTokenEstimate = 1000
)

// Protected tools that should not have their output pruned.
var pruneProtectedTools = map[string]bool{
	"skill": true,
}

// CompactionConfig controls compaction behavior.
type CompactionConfig struct {
	// Auto enables automatic compaction when context overflows. Default: true.
	Auto bool

	// Prune enables pruning of old tool outputs. Default: true.
	Prune bool

	// Agent is the agent to use for compaction. If nil, uses the model directly.
	// When set, the agent's system prompt is used for the compaction request.
	Agent CompactionAgent

	// PrunedMessage generates replacement text when content is pruned or excluded.
	// Called with metadata about the removed content. If nil, a default message is used.
	PrunedMessage func(info PrunedInfo) string
}

// PrunedInfo describes content that was pruned or excluded from context.
type PrunedInfo struct {
	Type     string // "tool_output", "image", "file"
	MimeType string // for images/files
	Filename string // FilePart.Filename
	Source   string // ImagePart.Source (e.g. S3 key)
}

// CompactionAgent defines the interface for a compaction agent.
type CompactionAgent interface {
	// SystemPrompt returns the system prompt for the compaction agent.
	SystemPrompt() string

	// Model returns the model ID to use for compaction, or empty to use session's model.
	Model() string
}

// DefaultCompactionConfig returns the default compaction configuration.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		Auto:  true,
		Prune: true,
		Agent: nil, // Uses model directly by default
	}
}

// DefaultPrunedMessage returns a generic replacement string for pruned content.
func DefaultPrunedMessage(info PrunedInfo) string {
	switch info.Type {
	case "tool_output":
		return "[Old tool result content cleared]"
	case "image":
		return "[Image removed from context]"
	case "file":
		if info.Filename != "" {
			return "[File " + info.Filename + " removed from context]"
		}
		return "[File removed from context]"
	default:
		return "[Content removed from context]"
	}
}

// EstimateTokens estimates the token count for a string using chars/4.
// This matches opencode's Token.estimate() function.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + CharsPerToken - 1) / CharsPerToken // Round up
}

// EstimateMessagesTokens returns a rough token estimate across a slice of
// messages. Used to compute post-compaction context size for UI display.
func EstimateMessagesTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content)
		for _, p := range m.Parts {
			total += EstimateTokens(p.Text)
			if p.Tool != nil {
				total += EstimateTokens(p.Tool.Input)
				total += EstimateTokens(p.Tool.Output)
			}
		}
	}
	return total
}

// SimpleCompactionAgent is a simple implementation of CompactionAgent.
type SimpleCompactionAgent struct {
	systemPrompt string
	model        string
}

// NewCompactionAgent creates a new compaction agent with the given system prompt and model.
func NewCompactionAgent(systemPrompt, model string) *SimpleCompactionAgent {
	return &SimpleCompactionAgent{
		systemPrompt: systemPrompt,
		model:        model,
	}
}

// SystemPrompt returns the system prompt for the compaction agent.
func (a *SimpleCompactionAgent) SystemPrompt() string {
	return a.systemPrompt
}

// Model returns the model ID to use for compaction.
func (a *SimpleCompactionAgent) Model() string {
	return a.model
}

// IsOverflow checks if the session has exceeded its context limit.
func (s *Session) IsOverflow() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check if auto compaction is disabled
	if !s.CompactionConfig.Auto {
		return false
	}

	if s.ModelLimits.Context == 0 {
		return false // No limit set
	}

	// Calculate total tokens used
	totalUsed := s.Tokens.Input + s.Tokens.Cache.Read + s.Tokens.Output

	// Calculate usable context (total - reserved output)
	outputReserve := s.ModelLimits.Output
	if outputReserve == 0 || outputReserve > OutputTokenMax {
		outputReserve = OutputTokenMax
	}

	usable := s.ModelLimits.Input
	if usable == 0 {
		usable = s.ModelLimits.Context - outputReserve
	}

	return totalUsed > usable
}

// Prune removes old tool outputs to reduce context size.
// Goes backwards through messages, keeping recent tool outputs
// and clearing older ones past the PruneProtect threshold.
func (s *Session) Prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if pruning is disabled
	if !s.CompactionConfig.Prune {
		return 0
	}

	type pruneTarget struct {
		tool *ToolPart // non-nil for tool parts
		part *Part     // non-nil for image/file parts
	}

	var total int
	var pruned int
	var toPrune []pruneTarget
	turns := 0

	// Go backwards through messages
loop:
	for msgIdx := len(s.Messages) - 1; msgIdx >= 0; msgIdx-- {
		msg := &s.Messages[msgIdx]

		// Count user message turns
		if msg.Role == "user" {
			turns++
		}

		// Skip first 2 turns (keep recent context)
		if turns < 2 {
			continue
		}

		// Stop at compaction summary
		if msg.Role == "assistant" && msg.Summary {
			break loop
		}

		// Process parts backwards
		for partIdx := len(msg.Parts) - 1; partIdx >= 0; partIdx-- {
			part := &msg.Parts[partIdx]

			switch part.Type {
			case "tool":
				if part.Tool == nil || part.Tool.Status != "completed" {
					continue
				}
				if pruneProtectedTools[part.Tool.Name] {
					continue
				}
				// Already compacted - stop here
				if part.Tool.Compacted {
					break loop
				}
				estimate := EstimateTokens(part.Tool.Output)
				total += estimate
				if total > PruneProtect {
					pruned += estimate
					toPrune = append(toPrune, pruneTarget{tool: part.Tool})
				}

			case "image":
				if part.Compacted || part.Image == nil {
					continue
				}
				estimate := ImageTokenEstimate
				total += estimate
				if total > PruneProtect {
					pruned += estimate
					toPrune = append(toPrune, pruneTarget{part: part})
				}

			case "file":
				if part.Compacted || part.File == nil {
					continue
				}
				estimate := FileTokenEstimate
				total += estimate
				if total > PruneProtect {
					pruned += estimate
					toPrune = append(toPrune, pruneTarget{part: part})
				}
			}
		}
	}

	// Only prune if we have enough to make a difference
	if pruned > PruneMinimum {
		prunedMsg := s.CompactionConfig.PrunedMessage
		if prunedMsg == nil {
			prunedMsg = DefaultPrunedMessage
		}

		for _, t := range toPrune {
			if t.tool != nil {
				t.tool.Compacted = true
			}
			if t.part != nil {
				// Replace image/file with text placeholder.
				info := PrunedInfo{Type: t.part.Type}
				if t.part.Image != nil {
					info.MimeType = t.part.Image.MimeType
					info.Source = t.part.Image.Source
				}
				if t.part.File != nil {
					info.MimeType = t.part.File.MimeType
					info.Filename = t.part.File.Filename
				}
				*t.part = Part{
					Type: "text",
					Text: prunedMsg(info),
				}
			}
		}
		return len(toPrune)
	}

	return 0
}

// CompactionPrompt is the default prompt for context compaction.
// This is appended as a user message to ask for a summary.
const CompactionPrompt = `Provide a detailed prompt for continuing our conversation above. Focus on information that would be helpful for continuing the conversation, including what we did, what we're doing, which files we're working on, and what we're going to do next considering new session will not have access to our conversation.`

// DefaultCompactionSystemPrompt is the system prompt used for the compaction agent.
// This matches opencode's agent/prompt/compaction.txt exactly (including trailing space on line 3 and trailing newline).
const DefaultCompactionSystemPrompt = "You are a helpful AI assistant tasked with summarizing conversations.\n\nWhen asked to summarize, provide a detailed but concise summary of the conversation. \nFocus on information that would be helpful for continuing the conversation, including:\n- What was done\n- What is currently being worked on\n- Which files are being modified\n- What needs to be done next\n- Key user requests, constraints, or preferences that should persist\n- Important technical decisions and why they were made\n\nYour summary should be comprehensive enough to provide context but concise enough to be quickly understood.\n"

// CompactionTriggerPrompt is added by opencode when a compaction part is converted to model messages.
// This matches MessageV2.toModelMessages() behavior for compaction parts.
const CompactionTriggerPrompt = "What did we do so far?"

// CompactOptions contains options for compaction requests.
// This matches opencode's behavior of passing the same provider options to compaction.
type CompactOptions struct {
	// ProviderOptions are provider-specific options (store, promptCacheKey, etc.)
	ProviderOptions map[string]any

	// MaxOutputTokens limits the compaction response length.
	MaxOutputTokens int
}

// Compact performs context compaction by asking the model to summarize.
// conversationMessages should be the current conversation history (from the runner).
// Returns the summary text that should be used to continue the conversation.
func (s *Session) Compact(ctx context.Context, model stream.Model, conversationMessages []goai.Message, opts *CompactOptions) (string, error) {
	s.mu.RLock()
	agent := s.CompactionConfig.Agent
	s.mu.RUnlock()

	// Build messages for compaction request
	var messages []goai.Message

	// Always add the compaction system prompt (matching opencode's compaction agent)
	systemPrompt := DefaultCompactionSystemPrompt
	if agent != nil && agent.SystemPrompt() != "" {
		systemPrompt = agent.SystemPrompt()
	}
	messages = append(messages, goai.NewSystemMessage(systemPrompt))

	// Add conversation history (skip the original system prompt, keep everything else)
	for _, msg := range conversationMessages {
		if msg.Role == "system" {
			continue // Skip original system prompt
		}
		messages = append(messages, msg)
	}

	// Add "What did we do so far?" message (matching opencode's MessageV2.toModelMessages for compaction parts)
	messages = append(messages, goai.NewUserMessage(CompactionTriggerPrompt))

	// Add compaction request
	messages = append(messages, goai.NewUserMessage(CompactionPrompt))

	// Call the model without tools (matching opencode's compaction processor)
	input := stream.Input{
		Model:    model,
		Messages: messages,
		// No tools - just generate text summary
	}

	// Apply provider options if provided (matching opencode's LLM.stream behavior)
	if opts != nil {
		if opts.ProviderOptions != nil {
			input.ProviderOptions = opts.ProviderOptions
		}
		if opts.MaxOutputTokens > 0 {
			input.MaxOutputTokens = &opts.MaxOutputTokens
		}
	}

	result, err := goai.StreamText(ctx, input)
	if err != nil {
		return "", fmt.Errorf("compaction stream error: %w", err)
	}

	// Collect the response
	var summary strings.Builder
	for event := range result.FullStream {
		if e, ok := event.Data.(stream.TextDeltaEvent); ok {
			summary.WriteString(e.Text)
		}
	}

	// Mark this as a compaction summary
	s.mu.Lock()
	s.Messages = append(s.Messages, Message{
		Role:    "assistant",
		Content: summary.String(),
		Summary: true,
	})
	s.mu.Unlock()

	// Publish compaction event on the session's bus
	if s.bus != nil {
		s.bus.Publish(bus.EventSessionCompacted, bus.SessionCompactedPayload{
			SessionID: s.ID,
		})
	}

	return summary.String(), nil
}

// CompactAndContinue performs compaction and adds a synthetic continue message.
// This is used for automatic compaction during the thinking loop.
// conversationMessages should be the current conversation history (from the runner).
func (s *Session) CompactAndContinue(ctx context.Context, model stream.Model, conversationMessages []goai.Message, opts *CompactOptions) error {
	// Find the last user message before compaction (to preserve it)
	var lastUserMsg *goai.Message
	for i := len(conversationMessages) - 1; i >= 0; i-- {
		if conversationMessages[i].Role == "user" {
			lastUserMsg = &conversationMessages[i]
			break
		}
	}

	summary, err := s.Compact(ctx, model, conversationMessages, opts)
	if err != nil {
		return err
	}

	// Rebuild s.Messages with: original user message + summary + continue
	// This matches opencode's filterCompacted behavior
	s.mu.Lock()
	s.Messages = nil // Clear existing messages

	// Add the original user message that triggered compaction
	if lastUserMsg != nil {
		content := lastUserMsg.Content.Text
		if content == "" && len(lastUserMsg.Content.Parts) > 0 {
			// Try to extract text from parts
			for _, part := range lastUserMsg.Content.Parts {
				if tp, ok := part.(goai.TextPart); ok {
					content = tp.Text
					break
				}
			}
		}
		s.Messages = append(s.Messages, Message{
			Role:    "user",
			Content: content,
		})
	}

	// Add the compaction summary
	s.Messages = append(s.Messages, Message{
		Role:    "assistant",
		Content: summary,
		Summary: true,
	})

	// Add synthetic continue message
	s.Messages = append(s.Messages, Message{
		Role:    "user",
		Content: "Continue if you have next steps",
	})
	s.mu.Unlock()

	return nil
}

// FilterCompacted returns messages up to and including the last compaction point.
// This prevents showing compacted context twice when rebuilding messages.
func (s *Session) FilterCompacted() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Find the last compaction point
	compactionIdx := -1
	for i := len(s.Messages) - 1; i >= 0; i-- {
		msg := s.Messages[i]
		if msg.Role == "assistant" && msg.Summary {
			compactionIdx = i
			break
		}
	}

	// If no compaction, return all messages
	if compactionIdx == -1 {
		result := make([]Message, len(s.Messages))
		copy(result, s.Messages)
		return result
	}

	// Return messages from compaction point onwards
	result := make([]Message, len(s.Messages)-compactionIdx)
	copy(result, s.Messages[compactionIdx:])
	return result
}
