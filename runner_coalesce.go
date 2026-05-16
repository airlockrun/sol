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
// Multipart user messages (images / files) are left as-is: concatenating
// their parts would be lossy and they aren't the adjacent-text case this
// guards. Output is always a fresh slice; the merged message is a copy,
// so the runner's retained r.messages is never mutated.
func coalesceConsecutiveUser(msgs []message.Message) []message.Message {
	if len(msgs) < 2 {
		return msgs
	}
	out := make([]message.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" && !m.Content.IsMultiPart() && len(out) > 0 {
			if last := &out[len(out)-1]; last.Role == "user" && !last.Content.IsMultiPart() {
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
