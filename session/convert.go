package session

import (
	"encoding/json"
	"fmt"

	"github.com/airlockrun/goai"
	"github.com/airlockrun/goai/message"
)

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
		case message.ImagePart:
			msg.Parts = append(msg.Parts, Part{
				Type: "image",
				Image: &ImagePart{
					Image:    v.Image,
					MimeType: v.MimeType,
				},
			})
		case message.FilePart:
			msg.Parts = append(msg.Parts, Part{
				Type: "file",
				File: &FilePart{
					Data:     v.Data,
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
					CallID: v.ToolCallID,
					Name:   v.ToolName,
					Output: resultToString(v.Result),
					Status: "completed",
				},
			})
		case message.ReasoningPart:
			msg.Parts = append(msg.Parts, Part{
				Type: "reasoning",
				Text: v.Text,
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
				case "tool":
					if p.Tool != nil {
						parts = append(parts, goai.ToolCallPart{
							ID:    p.Tool.CallID,
							Name:  p.Tool.Name,
							Input: json.RawMessage(p.Tool.Input),
						})
					}
				case "image":
					if p.Image != nil {
						parts = append(parts, message.ImagePart{
							Image:    p.Image.Image,
							MimeType: p.Image.MimeType,
						})
					}
				case "file":
					if p.File != nil {
						parts = append(parts, message.FilePart{
							Data:     p.File.Data,
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
			case "image":
				if p.Image != nil {
					extras = append(extras, message.ImagePart{
						Image:    p.Image.Image,
						MimeType: p.Image.MimeType,
					})
				}
			case "file":
				if p.File != nil {
					extras = append(extras, message.FilePart{
						Data:     p.File.Data,
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
			if len(extras) > 0 && !p.Tool.Compacted {
				toolResult := message.ToolResultPart{
					ToolCallID: p.Tool.CallID,
					ToolName:   p.Tool.Name,
					Result:     output,
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
					output,
					false,
				))
			}
		}
		return msgs
	}
	return nil
}

func resultToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
