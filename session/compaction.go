package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
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
	Source   string // FilePart.Source (e.g. S3 key)
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
			if p.Compacted {
				continue
			}
			total += EstimateTokens(p.Text)
			if p.Tool != nil {
				total += EstimateTokens(p.Tool.Input)
				total += estimateToolOutputTokens(p.Tool)
			}
			if p.File != nil {
				total += estimateFileTokens(p.File.MimeType)
			}
		}
	}
	return total
}

func estimateFileTokens(mimeType string) int {
	if strings.HasPrefix(mimeType, "image/") {
		return ImageTokenEstimate
	}
	return FileTokenEstimate
}

func estimateToolOutputTokens(p *ToolPart) int {
	output := toolOutputFromSession(p)
	if content, ok := output.(message.ContentOutput); ok {
		total := 0
		for _, item := range content.Value {
			if item.Type == "text" {
				total += EstimateTokens(item.Text)
			} else if strings.HasPrefix(item.Type, "image-") {
				total += ImageTokenEstimate
			} else {
				total += FileTokenEstimate
			}
		}
		return total
	}
	return EstimateTokens(message.ToolOutputWire(output))
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

	// An unknown budget cannot trigger automatic compaction. Callers requiring
	// a known budget can enforce it with ModelLimits.Validate before running.
	if s.ModelLimits.Context == 0 && s.ModelLimits.Input == 0 {
		return false
	}
	if err := s.ModelLimits.Validate(true); err != nil {
		panic(err)
	}

	// Calculate total tokens used
	totalUsed := s.Tokens.Input + s.Tokens.Cache.Read + s.Tokens.Output

	return totalUsed > s.ModelLimits.inputBudget()
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
		if msg.Role == "user" && !msg.Generated && !isInputContext(s.Messages, msgIdx) {
			turns++
		}

		// Skip first 2 turns (keep recent context)
		if turns < 2 || (msg.Role == "user" && turns == 2) {
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
				estimate := estimateToolOutputTokens(part.Tool)
				total += estimate
				if total > PruneProtect {
					pruned += estimate
					toPrune = append(toPrune, pruneTarget{tool: part.Tool})
				}

			case "file":
				if part.Compacted || part.File == nil {
					continue
				}
				estimate := estimateFileTokens(part.File.MimeType)
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
		before := EstimateMessagesTokens(s.Messages)
		prunedMsg := s.CompactionConfig.PrunedMessage
		if prunedMsg == nil {
			prunedMsg = DefaultPrunedMessage
		}

		for _, t := range toPrune {
			if t.tool != nil {
				t.tool.Compacted = true
				t.tool.Output = prunedMsg(PrunedInfo{Type: "tool_output"})
				t.tool.OutputType = "text"
				if t.tool.Outcome == "error" {
					t.tool.OutputType = "error-text"
				}
				if t.tool.Outcome == "denied" {
					t.tool.OutputType = "execution-denied"
				}
			}
			if t.part != nil {
				// Replace image/file with text placeholder.
				info := PrunedInfo{Type: t.part.Type}
				if t.part.File != nil {
					info.MimeType = t.part.File.MimeType
					info.Filename = t.part.File.Filename
					info.Source = t.part.File.Source
					if strings.HasPrefix(info.MimeType, "image/") {
						info.Type = "image"
					}
				}
				*t.part = Part{
					Type: "text",
					Text: prunedMsg(info),
				}
			}
		}
		freed := before - EstimateMessagesTokens(s.Messages)
		s.Tokens = Tokens{Input: max(0, s.Tokens.Input+s.Tokens.Cache.Read+s.Tokens.Output-freed)}
		return len(toPrune)
	}

	return 0
}

// CompactionPrompt is the default prompt for context compaction.
// This is appended as a user message to ask for a summary.
const CompactionPrompt = `Summarize the preceding history as background context. Preserve user constraints, decisions, completed actions and their outcomes, and unresolved work. The latest user turn is retained separately; do not invent or restate it. Completed actions are records, not instructions to execute them again.`

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

	// TransformMessages runs on the complete request, including the compaction
	// system prompt. Callers use it to enforce outbound redaction.
	TransformMessages func([]goai.Message) []goai.Message
}

// Compact asks the model to summarize the supplied history without changing the
// canonical transcript. CompactAndContinue applies prefix compaction.
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
		if opts.TransformMessages != nil {
			input.Messages = opts.TransformMessages(input.Messages)
		}
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

	for range result.FullStream {
	}
	summary, err := result.Text()
	if err != nil {
		return "", fmt.Errorf("compaction stream error: %w", err)
	}
	finish, err := result.FinishReason()
	if err != nil {
		return "", fmt.Errorf("compaction finish: %w", err)
	}
	if finish != stream.FinishReasonStop {
		return "", fmt.Errorf("compaction summary incomplete: finish reason %q", finish)
	}
	if strings.TrimSpace(summary) == "" {
		return "", errors.New("compaction summary is empty")
	}

	return summary, nil
}

// isInputContext identifies user-level data associated with a following human
// input. Text and provider roles cannot establish this association.
func isInputContext(messages []Message, i int) bool {
	msg := messages[i]
	return msg.Role == "user" && msg.ParentID != "" && i+1 < len(messages) &&
		messages[i+1].Role == "user" && messages[i+1].ID == msg.ParentID
}

// CompactAndContinue summarizes only history before the latest human input.
// Its associated context and complete assistant/tool suffix remain canonical.
// A summary alone is not eligible history for another compaction.
func (s *Session) CompactAndContinue(ctx context.Context, model stream.Model, opts *CompactOptions) error {
	history := s.GetMessages()
	start := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" && !history[i].Generated && !isInputContext(history, i) {
			start = i
			if i > 0 && isInputContext(history, i-1) {
				start--
			}
			break
		}
	}
	if start < 0 {
		return errors.New("compaction requires a human input boundary")
	}
	eligible := false
	var systems []Message
	for _, msg := range history[:start] {
		if msg.Role == "system" {
			systems = append(systems, msg)
		} else if !msg.Summary {
			eligible = true
		}
	}
	if !eligible {
		return errors.New("no older history to compact; latest turn must remain intact")
	}
	summary, err := s.Compact(ctx, model, MessagesToGoAI(history[:start]), opts)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.Messages = append(systems, Message{
		ID:      rand.Text(),
		Role:    "assistant",
		Content: summary,
		Summary: true,
	})

	s.Messages = append(s.Messages, history[start:]...)
	s.Tokens = Tokens{Input: EstimateMessagesTokens(s.Messages)}
	s.mu.Unlock()
	if s.bus != nil {
		s.bus.Publish(bus.EventSessionCompacted, bus.SessionCompactedPayload{SessionID: s.ID})
	}

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
