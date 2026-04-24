package bus

// Event type constants - matches opencode's event types.
const (
	// Question events
	QuestionAsked    = "question.asked"
	QuestionReplied  = "question.replied"
	QuestionRejected = "question.rejected"

	// Permission events
	PermissionAsked    = "permission.asked"
	PermissionReplied  = "permission.replied"
	PermissionRejected = "permission.rejected"

	// Session events
	SessionCreated   = "session.created"
	SessionUpdated   = "session.updated"
	SessionCompacted = "session.compacted"

	// Aliases for code clarity
	EventSessionUpdated   = SessionUpdated
	EventSessionCompacted = SessionCompacted

	// Message events
	MessageCreated = "message.created"
	MessageUpdated = "message.updated"

	// Tool events
	ToolStarted   = "tool.started"
	ToolCompleted = "tool.completed"

	// Stream events (published by runner during LLM streaming)
	StreamTextDelta    = "stream.text_delta"
	StreamToolCall     = "stream.tool_call"
	StreamToolResult   = "stream.tool_result"
	StreamStepComplete = "stream.step_complete"
)

// QuestionAskedPayload is the payload for question.asked events.
type QuestionAskedPayload struct {
	ID        string         `json:"id"`
	SessionID string         `json:"sessionID"`
	Questions []QuestionInfo `json:"questions"`
	Tool      *ToolContext   `json:"tool,omitempty"`
}

// QuestionInfo represents a single question.
type QuestionInfo struct {
	Question string           `json:"question"`
	Header   string           `json:"header"`
	Options  []QuestionOption `json:"options"`
	Multiple bool             `json:"multiple,omitempty"`
	Custom   bool             `json:"custom,omitempty"`
}

// QuestionOption represents a choice option.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ToolContext provides context about which tool call triggered the question.
type ToolContext struct {
	MessageID string `json:"messageID"`
	CallID    string `json:"callID"`
}

// QuestionRepliedPayload is the payload for question.replied events.
type QuestionRepliedPayload struct {
	SessionID string     `json:"sessionID"`
	RequestID string     `json:"requestID"`
	Answers   [][]string `json:"answers"`
}

// QuestionRejectedPayload is the payload for question.rejected events.
type QuestionRejectedPayload struct {
	SessionID string `json:"sessionID"`
	RequestID string `json:"requestID"`
}

// PermissionAskedPayload is the payload for permission.asked events.
type PermissionAskedPayload struct {
	ID         string         `json:"id"`
	SessionID  string         `json:"sessionID"`
	Permission string         `json:"permission"`
	Patterns   []string       `json:"patterns"`
	Always     []string       `json:"always,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
}

// PermissionRepliedPayload is the payload for permission.replied events.
type PermissionRepliedPayload struct {
	SessionID string `json:"sessionID"`
	RequestID string `json:"requestID"`
	Response  string `json:"response"` // "once", "always", "reject"
}

// SessionUpdatedPayload is the payload for session.updated events.
type SessionUpdatedPayload struct {
	SessionID string `json:"sessionID"`
}

// SessionCompactedPayload is the payload for session.compacted events.
type SessionCompactedPayload struct {
	SessionID string `json:"sessionID"`
}

// ErrPermissionNeeded is returned when no permission rule matches.
// Implements FatalToolError so the executor propagates it up.
type ErrPermissionNeeded struct {
	Permission string         `json:"permission"`
	Patterns   []string       `json:"patterns"`
	ToolCallID string         `json:"toolCallID"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

func (e *ErrPermissionNeeded) Error() string      { return "permission needed: " + e.Permission }
func (e *ErrPermissionNeeded) FatalToolError() bool { return true }

// ErrQuestionNeeded is returned when no pre-loaded answer is available.
type ErrQuestionNeeded struct {
	Questions  []QuestionInfo `json:"questions"`
	ToolCallID string         `json:"toolCallID"`
}

func (e *ErrQuestionNeeded) Error() string      { return "question needs answer" }
func (e *ErrQuestionNeeded) FatalToolError() bool { return true }
