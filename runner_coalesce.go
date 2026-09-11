package sol

import "github.com/airlockrun/goai/message"

// coalesceConsecutiveUser merges runs of consecutive text-only user
// messages into a single message, joined by a blank line.
//
// Why: parts of the system legitimately emit two adjacent user turns
// (e.g. the human's prompt followed by a separate, UI-hidden context
// message). The Anthropic adapter already collapses same-role runs, but
// the OpenAI-compatible adapter is a 1:1 pass-through and several models
// on it (notably deepseek-reasoner) reject two user messages in a row.
// Collapsing here, at the single point where messages leave for the
// model, makes every provider behave the same.
//
// Messages with provider options and multipart messages are left as-is:
// merging must not change the scope of metadata or attachment parts.
// Merged messages are copies, so the retained transcript is never mutated.
func coalesceConsecutiveUser(msgs []message.Message) []message.Message {
	if len(msgs) < 2 {
		return msgs
	}
	out := make([]message.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" && !m.Content.IsMultiPart() && len(m.ProviderOptions) == 0 && len(out) > 0 {
			if last := &out[len(out)-1]; last.Role == "user" && !last.Content.IsMultiPart() && len(last.ProviderOptions) == 0 {
				switch {
				case last.Content.Text == "":
					last.Content.Text = m.Content.Text
				case m.Content.Text != "":
					last.Content.Text += "\n\n" + m.Content.Text
				}
				continue
			}
		}
		out = append(out, m)
	}
	return out
}
