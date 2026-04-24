// Package session manages the agent session lifecycle including
// message history, token tracking, and context compaction.
package session

import (
	"context"
	"sync"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/stream"
	"github.com/airlockrun/sol/bus"
)

// Session represents an active agent session.
type Session struct {
	mu sync.RWMutex

	ID        string
	AgentName string
	ModelID   string

	// Message history
	Messages []Message

	// Token tracking
	Tokens Tokens

	// Model limits for overflow detection
	ModelLimits ModelLimits

	// Compaction configuration
	CompactionConfig CompactionConfig

	// Scoped event bus
	bus *bus.Bus

	// Context and cancellation
	ctx    context.Context
	cancel context.CancelFunc
}

// Tokens tracks token usage across the session.
type Tokens struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
	Cache     struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

// ModelLimits defines the model's context window limits.
type ModelLimits struct {
	Context int // Total context window size
	Input   int // Max input tokens (if different from context - output)
	Output  int // Max output tokens
}

// Message represents a message in the session history.
type Message struct {
	ID        string `json:"id"`
	Role      string `json:"role"`                // "system", "user", "assistant", "tool"
	Content   string `json:"content,omitempty"`
	Parts     []Part `json:"parts,omitempty"`
	ParentID  string `json:"parentId,omitempty"`  // For assistant messages, links to user message
	Summary   bool   `json:"summary,omitempty"`   // True if this is a compaction summary
	Tokens    Tokens `json:"tokens,omitempty"`    // Token usage for this message
	Compacted bool   `json:"compacted,omitempty"` // True if tool outputs have been pruned
}

// Part represents a part of a message (text, tool call, image, file, etc.)
type Part struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`              // "text", "tool", "reasoning", "compaction", "image", "file"
	Text      string     `json:"text,omitempty"`
	Tool      *ToolPart  `json:"tool,omitempty"`
	Image     *ImagePart `json:"image,omitempty"`
	File      *FilePart  `json:"file,omitempty"`
	Compacted bool       `json:"compacted,omitempty"` // True if output has been pruned
}

// ToolPart represents a tool call and its result.
type ToolPart struct {
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	Input     string `json:"input,omitempty"`
	Output    string `json:"output,omitempty"`
	Status    string `json:"status"`              // "pending", "running", "completed", "error"
	Compacted bool   `json:"compacted,omitempty"` // True if output has been pruned
}

// ImagePart represents an image attachment (base64 or URL).
type ImagePart struct {
	Image    string `json:"image"`              // base64 data or URL
	MimeType string `json:"mimeType,omitempty"` // e.g., "image/png"
	// Source identifies where this image came from (e.g. a file key, URL, or ID).
	// Session-level metadata only — not passed to goai or LLM providers.
	Source string `json:"source,omitempty"`
}

// FilePart represents a file attachment (base64-encoded).
type FilePart struct {
	Data     string `json:"data"`               // base64-encoded content
	MimeType string `json:"mimeType"`           // e.g., "application/pdf"
	Filename string `json:"filename,omitempty"` // optional filename
	// Source identifies where this file came from (e.g. a file key, URL, or ID).
	// Session-level metadata only — not passed to goai or LLM providers.
	Source string `json:"source,omitempty"`
}

// SessionOptions contains options for creating a new session.
type SessionOptions struct {
	ID               string
	AgentName        string
	ModelID          string
	Limits           ModelLimits
	CompactionConfig *CompactionConfig // nil = use defaults
	Bus              *bus.Bus
}

// New creates a new session.
func New(id, agentName, modelID string, limits ModelLimits) *Session {
	return NewWithOptions(SessionOptions{
		ID:        id,
		AgentName: agentName,
		ModelID:   modelID,
		Limits:    limits,
	})
}

// NewWithOptions creates a new session with options.
func NewWithOptions(opts SessionOptions) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	compactionConfig := DefaultCompactionConfig()
	if opts.CompactionConfig != nil {
		compactionConfig = *opts.CompactionConfig
	}

	return &Session{
		ID:               opts.ID,
		AgentName:        opts.AgentName,
		ModelID:          opts.ModelID,
		ModelLimits:      opts.Limits,
		CompactionConfig: compactionConfig,
		bus:              opts.Bus,
		ctx:              ctx,
		cancel:           cancel,
	}
}

// Context returns the session context.
func (s *Session) Context() context.Context {
	return s.ctx
}

// Cancel cancels the session.
func (s *Session) Cancel() {
	s.cancel()
}

// AddMessage adds a message to the session history.
func (s *Session) AddMessage(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = append(s.Messages, msg)
}

// UpdateTokens updates the session token counts from a step result.
// Note: We replace (not accumulate) because each API response's input
// tokens already includes ALL messages in the conversation, not just
// the new ones.
func (s *Session) UpdateTokens(usage stream.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Tokens.Input = usage.InputTotal()
	s.Tokens.Output = usage.OutputTotal()
}

// GetMessages returns all messages in the session.
func (s *Session) GetMessages() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Message, len(s.Messages))
	copy(result, s.Messages)
	return result
}

// ToGoAIMessages converts session messages to goai.Message format.
func (s *Session) ToGoAIMessages() []goai.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return MessagesToGoAI(s.Messages)
}

// PublishUpdate publishes a session update event on the session's bus.
func (s *Session) PublishUpdate() {
	if s.bus == nil {
		return
	}
	s.bus.Publish(bus.EventSessionUpdated, bus.SessionUpdatedPayload{
		SessionID: s.ID,
	})
}
