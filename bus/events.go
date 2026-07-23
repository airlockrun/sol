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

	// Automatic compaction lifecycle events
	AutomaticCompactionStarted  = "automatic_compaction.started"
	AutomaticCompactionFinished = "automatic_compaction.finished"
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

// AutomaticCompactionStartedPayload marks the start of an overflow-triggered
// compaction attempt.
type AutomaticCompactionStartedPayload struct{}

// AutomaticCompactionFinishedPayload describes the result of an
// overflow-triggered compaction attempt.
type AutomaticCompactionFinishedPayload struct {
	TokensFreed int    `json:"tokensFreed"`
	Error       string `json:"error"`
}

// ErrPermissionNeeded is returned when no permission rule matches.
// Implements FatalToolError so the executor propagates it up.
type ErrPermissionNeeded struct {
	Permission string         `json:"permission"`
	Patterns   []string       `json:"patterns"`
	ToolCallID string         `json:"toolCallID"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

func (e *ErrPermissionNeeded) Error() string        { return "permission needed: " + e.Permission }
func (e *ErrPermissionNeeded) FatalToolError() bool { return true }

// ErrQuestionNeeded is returned when no pre-loaded answer is available.
type ErrQuestionNeeded struct {
	Questions  []QuestionInfo `json:"questions"`
	ToolCallID string         `json:"toolCallID"`
}

func (e *ErrQuestionNeeded) Error() string        { return "question needs answer" }
func (e *ErrQuestionNeeded) FatalToolError() bool { return true }

// ErrDelegatedSuspend is returned by a tool whose work was delegated to
// a child execution that itself suspended (a Sol subagent via the Task
// tool, or a cross-container A2A sibling run). The parent step suspends
// carrying this so the suspension propagates up the run tree to the
// root (where a human resolves it) and the decision cascades back down.
//
// Child is opaque to bus/runner — only the resume dispatcher (agentsdk)
// interprets it, keyed by Transport:
//   - "inprocess": the suspended subagent's reconstruction state
//     (messages + its own SuspensionContext), nested so the parent
//     checkpoint is self-contained.
//   - "a2a": an opaque cross-container handle, e.g. {agentId, taskId},
//     which airlock resolves to a ResumeRunID downstream.
//
// FatalToolError so the executor propagates it to handleSuspension
// exactly like a permission/question gate.
type ErrDelegatedSuspend struct {
	ToolCallID string `json:"toolCallID"`
	Transport  string `json:"transport"`
	Child      any    `json:"child"`
}

func (e *ErrDelegatedSuspend) Error() string {
	return "delegated execution suspended (" + e.Transport + ")"
}
func (e *ErrDelegatedSuspend) FatalToolError() bool { return true }
