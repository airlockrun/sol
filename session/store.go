package session

import "context"

// SessionStore provides pluggable persistence for conversation messages.
// Each store instance is pre-scoped to a single conversation by the caller.
// Sol doesn't know about conversation IDs, tenants, or routing.
type SessionStore interface {
	// Load returns the messages that should form the LLM context for the next
	// turn. Implementations may hide pre-checkpoint history here — Sol only
	// sees what the store decides to return. Returns empty slice (not error)
	// if the conversation has no post-checkpoint messages yet.
	Load(ctx context.Context) ([]Message, error)

	// Append persists new messages from the current turn.
	// Called after each step with the messages produced during that step.
	Append(ctx context.Context, msgs []Message) error

	// Compact atomically records a compaction: the summary messages become
	// the head of the new context window, and the store advances its
	// checkpoint so that subsequent Load calls return only `summary` + any
	// later Appends. Pre-checkpoint messages are not deleted — the store
	// decides how to keep them available for UI / audit.
	// tokensFreed is the delta between pre- and post-compaction input tokens,
	// recorded so the UI can show how much context was freed.
	Compact(ctx context.Context, summary []Message, tokensFreed int) error
}

// MemoryStore is a no-op store for CLI mode and testing.
// Messages live only in the session's Messages slice; nothing is persisted.
type MemoryStore struct{}

func (MemoryStore) Load(context.Context) ([]Message, error)              { return nil, nil }
func (MemoryStore) Append(context.Context, []Message) error              { return nil }
func (MemoryStore) Compact(context.Context, []Message, int) error        { return nil }
