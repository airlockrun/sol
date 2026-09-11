package sol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/airlockrun/goai/message"
)

// redactMessages returns a copy of msgs with text content stripped of
// any sensitive substrings via redactor. ToolCallPart.Input and
// ToolResultPart.Output are scanned too — agents that echo a secret in
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
		v.Input = redactJSON(v.Input, redactor)
		return v
	case message.ToolResultPart:
		switch o := v.Output.(type) {
		case message.JSONOutput:
			o.Value = redactJSON(o.Value, redactor)
			v.Output = o
		case message.ErrorJSONOutput:
			o.Value = redactJSON(o.Value, redactor)
			v.Output = o
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
			o.Value = slices.Clone(o.Value)
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

// Redact string values inside JSON, preserving numbers and escaping replacements.
func redactJSON(value any, redactor func(string) string) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Errorf("redact JSON: %w", err))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		panic(fmt.Errorf("redact JSON: %w", err))
	}
	var redact func(any) any
	redact = func(v any) any {
		switch x := v.(type) {
		case string:
			return redactor(x)
		case []any:
			for i := range x {
				x[i] = redact(x[i])
			}
		case map[string]any:
			result := make(map[string]any, len(x))
			for k, item := range x {
				result[redactor(k)] = redact(item)
			}
			return result
		}
		return v
	}
	raw, err = json.Marshal(redact(value))
	if err != nil {
		panic(fmt.Errorf("redact JSON: %w", err))
	}
	return raw
}
