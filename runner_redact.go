package sol

import (
	"github.com/airlockrun/goai/message"
)

// redactMessages returns a copy of msgs with text content stripped of
// any sensitive substrings via redactor. ToolCallPart.Input and
// ToolResultPart.Result are scanned too — agents that echo a secret in
// a tool argument or return value shouldn't leak it into the next
// step's history. nil redactor → return msgs unchanged.
//
// Best-effort substring replacement, mirroring agentsdk's redactor
// shape. Image / file / approval parts pass through untouched (their
// payloads are URL/base64 references).
func redactMessages(msgs []message.Message, redactor func(string) string) []message.Message {
	if redactor == nil || len(msgs) == 0 {
		return msgs
	}
	out := make([]message.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		if !m.Content.IsMultiPart() {
			out[i].Content.Text = redactor(m.Content.Text)
			continue
		}
		parts := make([]message.Part, len(m.Content.Parts))
		for j, p := range m.Content.Parts {
			parts[j] = redactPart(p, redactor)
		}
		out[i].Content.Parts = parts
	}
	return out
}

func redactPart(p message.Part, redactor func(string) string) message.Part {
	switch v := p.(type) {
	case message.TextPart:
		v.Text = redactor(v.Text)
		return v
	case message.ReasoningPart:
		v.Text = redactor(v.Text)
		return v
	case message.ToolCallPart:
		// Input is a json.RawMessage; redact as bytes and trust it's
		// still valid JSON (substring replace doesn't break shape since
		// [REDACTED] is non-special).
		v.Input = []byte(redactor(string(v.Input)))
		return v
	case message.ToolResultPart:
		// Redact the text-bearing output variants; JSON variants pass
		// through (agents packing secrets into typed output opt into
		// manual redaction).
		switch o := v.Output.(type) {
		case message.TextOutput:
			o.Value = redactor(o.Value)
			v.Output = o
		case message.ErrorTextOutput:
			o.Value = redactor(o.Value)
			v.Output = o
		case message.ExecutionDeniedOutput:
			o.Reason = redactor(o.Reason)
			v.Output = o
		case message.ContentOutput:
			for i := range o.Value {
				if o.Value[i].Type == "text" {
					o.Value[i].Text = redactor(o.Value[i].Text)
				}
			}
			v.Output = o
		}
		return v
	}
	return p
}

// redactString is a small adaptor — applies the redactor when non-nil.
func redactString(s string, redactor func(string) string) string {
	if redactor == nil {
		return s
	}
	return redactor(s)
}
