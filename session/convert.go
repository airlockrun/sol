package session

import (
	"encoding/json"
	"strings"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
)

// stringFromFileData extracts the raw payload string from a goai FileData
// union for the session model's flat string fields.
func stringFromFileData(d message.FileData) string {
	switch v := d.(type) {
	case message.FileDataBytes:
		return v.Data
	case message.FileDataURL:
		return v.URL
	case message.FileDataText:
		return v.Text
	default:
		return ""
	}
}

// fileDataFromString rebuilds a goai FileData union from a session model's
// flat string: http(s) and data URLs become a URL reference, everything else
// is treated as inline base64 bytes.
func fileDataFromString(s string) message.FileData {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "data:") {
		return message.FileDataURL{URL: s}
	}
	return message.FileDataBytes{Data: s}
}

// FromGoAIMessages converts goai messages to session messages.
func FromGoAIMessages(msgs []goai.Message) []Message {
	result := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		result = append(result, FromGoAIMessage(m))
	}
	return result
}

// FromGoAIMessage converts a single goai message to a session message.
func FromGoAIMessage(m goai.Message) Message {
	msg := Message{
		Role: string(m.Role),
	}

	if !m.Content.IsMultiPart() {
		msg.Content = m.Content.Text
		return msg
	}

	for _, p := range m.Content.Parts {
		switch v := p.(type) {
		case message.TextPart:
			msg.Parts = append(msg.Parts, Part{
				Type: "text",
				Text: v.Text,
			})
		case message.FilePart:
			msg.Parts = append(msg.Parts, Part{
				Type: "file",
				File: &FilePart{
					Data:     stringFromFileData(v.Data),
					MimeType: v.MimeType,
					Filename: v.Filename,
				},
			})
		case message.ToolCallPart:
			msg.Parts = append(msg.Parts, Part{
				Type: "tool",
				Tool: &ToolPart{
					CallID: v.ID,
					Name:   v.Name,
					Input:  string(v.Input),
					Status: "pending",
				},
			})
		case message.ToolResultPart:
			msg.Parts = append(msg.Parts, Part{
				Type: "tool",
				Tool: &ToolPart{
					CallID:  v.ToolCallID,
					Name:    v.ToolName,
					Output:  message.ToolOutputWire(v.Output),
					Status:  "completed",
					Outcome: message.ToolOutcome(v.Output),
				},
			})
			// A content output carries file/image items inline; surface them
			// as session image/file parts so the CLI can show attachments.
			if co, ok := v.Output.(message.ContentOutput); ok {
				for _, it := range co.Value {
					switch it.Type {
					case "image-data", "image-url", "file-data", "file-url":
						data := it.Data
						if data == "" {
							data = it.URL
						}
						msg.Parts = append(msg.Parts, Part{
							Type: "file",
							File: &FilePart{Data: data, MimeType: it.MediaType, Filename: it.Filename, Source: it.Filename},
						})
					}
				}
			}
		case message.ReasoningPart:
			msg.Parts = append(msg.Parts, Part{
				Type:            "reasoning",
				Text:            v.Text,
				ProviderOptions: v.ProviderOptions,
			})
		}
	}

	return msg
}

// MessagesToGoAI converts session messages to goai format.
// This is the standalone version of Session.ToGoAIMessages().
func MessagesToGoAI(msgs []Message) []goai.Message {
	var result []goai.Message
	for _, msg := range msgs {
		result = append(result, MessageToGoAI(msg)...)
	}
	return result
}

// MessageToGoAI converts a single session message to one or more goai messages.
// Tool-role messages may produce multiple goai messages (one per tool result).
func MessageToGoAI(msg Message) []goai.Message {
	switch msg.Role {
	case "system":
		return []goai.Message{goai.NewSystemMessage(msg.Content)}
	case "user":
		return []goai.Message{goai.NewUserMessage(msg.Content)}
	case "assistant":
		if len(msg.Parts) > 0 {
			var parts []goai.Part
			for _, p := range msg.Parts {
				if p.Compacted {
					continue
				}
				switch p.Type {
				case "text":
					if p.Text != "" {
						parts = append(parts, goai.TextPart{Text: p.Text})
					}
				case "reasoning":
					parts = append(parts, goai.ReasoningPart{
						Text:            p.Text,
						ProviderOptions: p.ProviderOptions,
					})
				case "tool":
					if p.Tool != nil {
						parts = append(parts, goai.ToolCallPart{
							ID:    p.Tool.CallID,
							Name:  p.Tool.Name,
							Input: json.RawMessage(p.Tool.Input),
						})
					}
				case "file":
					if p.File != nil {
						parts = append(parts, message.FilePart{
							Data:     fileDataFromString(p.File.Data),
							MimeType: p.File.MimeType,
							Filename: p.File.Filename,
						})
					}
				}
			}
			if len(parts) > 0 {
				return []goai.Message{goai.NewAssistantMessageWithParts(parts...)}
			}
		} else if msg.Content != "" {
			return []goai.Message{goai.NewAssistantMessage(msg.Content)}
		}
	case "tool":
		var msgs []goai.Message
		// Collect non-compacted attachments and text parts for this tool message.
		var extras []goai.Part
		for _, p := range msg.Parts {
			if p.Compacted {
				continue
			}
			switch p.Type {
			case "text":
				if p.Text != "" {
					extras = append(extras, message.TextPart{Text: p.Text})
				}
			case "file":
				if p.File != nil {
					extras = append(extras, message.FilePart{
						Data:     fileDataFromString(p.File.Data),
						MimeType: p.File.MimeType,
						Filename: p.File.Filename,
					})
				}
			}
		}

		for _, p := range msg.Parts {
			if p.Type != "tool" || p.Tool == nil {
				continue
			}
			output := p.Tool.Output
			if p.Tool.Compacted {
				output = "[Old tool result content cleared]"
			}
			// Rebuild the discriminated outcome (error/denied/success) so it
			// survives the round-trip — the dot and provider is_error depend
			// on it. JSON fidelity is intentionally not preserved; the wire
			// string is what providers send anyway.
			toolOut := toolOutputFromSession(p.Tool.Outcome, output)
			if len(extras) > 0 && !p.Tool.Compacted {
				toolResult := message.ToolResultPart{
					ToolCallID: p.Tool.CallID,
					ToolName:   p.Tool.Name,
					Output:     toolOut,
				}
				allParts := []goai.Part{toolResult}
				allParts = append(allParts, extras...)
				msgs = append(msgs, goai.Message{
					Role:    "tool",
					Content: message.Content{Parts: allParts},
				})
			} else {
				msgs = append(msgs, goai.NewToolMessage(
					p.Tool.CallID,
					p.Tool.Name,
					toolOut,
				))
			}
		}
		return msgs
	}
	return nil
}

// toolOutputFromSession rebuilds a discriminated ToolResultOutput from the
// session ToolPart's structured outcome + flattened output string.
func toolOutputFromSession(outcome, output string) message.ToolResultOutput {
	switch outcome {
	case "error":
		return message.ErrorTextOutput{Value: output}
	case "denied":
		return message.ExecutionDeniedOutput{Reason: output}
	default:
		return message.TextOutput{Value: output}
	}
}
