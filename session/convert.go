package session

import (
	"encoding/json"
	"fmt"
	"strings"

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

// FromGoAIMessage preserves message and part order, provider metadata, and
// discriminated file and tool output types for persistence and replay.
func FromGoAIMessage(m goai.Message) Message {
	msg := Message{Role: string(m.Role), ProviderOptions: m.ProviderOptions, Content: m.Content.Text}
	for _, p := range m.Content.Parts {
		var part Part
		switch v := p.(type) {
		case message.TextPart:
			part = Part{Type: "text", Text: v.Text, ProviderOptions: v.ProviderOptions}
		case message.ReasoningPart:
			part = Part{Type: "reasoning", Text: v.Text, ProviderOptions: v.ProviderOptions}
		case message.FilePart:
			f := &FilePart{MimeType: v.MimeType, Filename: v.Filename}
			switch d := v.Data.(type) {
			case message.FileDataBytes:
				f.DataType, f.Data = "data", d.Data
				if source, ok := strings.CutPrefix(d.Data, "s3ref:"); ok {
					f.Source = source
				}
			case message.FileDataURL:
				f.DataType, f.Data = "url", d.URL
				if source, ok := strings.CutPrefix(d.URL, "s3ref:"); ok {
					f.Source = source
				}
			case message.FileDataText:
				f.DataType, f.Data = "text", d.Text
			case message.FileDataReference:
				f.DataType, f.Reference = "reference", d.Reference
			default:
				panic(fmt.Sprintf("session: unsupported file data %T", v.Data))
			}
			part = Part{Type: "file", File: f, ProviderOptions: v.ProviderOptions}
		case message.ToolCallPart:
			part = Part{Type: "tool", ProviderOptions: v.ProviderOptions, Tool: &ToolPart{
				CallID: v.ID, Name: v.Name, Input: string(v.Input), Status: "pending", ProviderExecuted: v.ProviderExecuted,
			}}
		case message.ToolResultPart:
			tp := &ToolPart{CallID: v.ToolCallID, Name: v.ToolName, Status: "completed", Result: true,
				ProviderExecuted: v.ProviderExecuted, Outcome: message.ToolOutcome(v.Output), Output: message.ToolOutputWire(v.Output)}
			switch o := v.Output.(type) {
			case message.TextOutput:
				tp.OutputType, tp.OutputProviderOptions = "text", o.ProviderOptions
			case message.ErrorTextOutput:
				tp.OutputType, tp.OutputProviderOptions = "error-text", o.ProviderOptions
			case message.JSONOutput:
				tp.OutputType, tp.OutputProviderOptions = "json", o.ProviderOptions
			case message.ErrorJSONOutput:
				tp.OutputType, tp.OutputProviderOptions = "error-json", o.ProviderOptions
			case message.ExecutionDeniedOutput:
				tp.OutputType, tp.OutputProviderOptions, tp.Output = "execution-denied", o.ProviderOptions, o.Reason
			case message.ContentOutput:
				tp.OutputType = "content"
			default:
				panic(fmt.Sprintf("session: unsupported tool output %T", v.Output))
			}
			// Validate structured values rather than accepting a failed wire marshal.
			if _, err := message.MarshalOutput(v.Output); err != nil {
				panic(fmt.Errorf("session: tool output: %w", err))
			}
			part = Part{Type: "tool", Tool: tp, ProviderOptions: v.ProviderOptions}
		case message.ToolApprovalRequestPart:
			part = Part{Type: "tool-approval-request", ApprovalRequest: &v}
		case message.ToolApprovalResponsePart:
			part = Part{Type: "tool-approval-response", ApprovalResponse: &v}
		default:
			panic(fmt.Sprintf("session: unsupported message part %T", p))
		}
		msg.Parts = append(msg.Parts, part)
	}
	return msg
}

// MessagesToGoAI converts session messages to goai format.
func MessagesToGoAI(msgs []Message) []goai.Message {
	var result []goai.Message
	for _, msg := range msgs {
		result = append(result, MessageToGoAI(msg)...)
	}
	return result
}

// MessageToGoAI preserves multipart content on every role, including multiple
// tool results in a single message. Compacted parts are omitted from replay.
func MessageToGoAI(msg Message) []goai.Message {
	m := goai.Message{Role: message.Role(msg.Role), ProviderOptions: msg.ProviderOptions, Content: message.Content{Text: msg.Content}}
	for _, p := range msg.Parts {
		if p.Compacted {
			continue
		}
		switch p.Type {
		case "text":
			m.Content.Parts = append(m.Content.Parts, message.TextPart{Text: p.Text, ProviderOptions: p.ProviderOptions})
		case "reasoning":
			m.Content.Parts = append(m.Content.Parts, message.ReasoningPart{Text: p.Text, ProviderOptions: p.ProviderOptions})
		case "file":
			if p.File == nil {
				panic("session: file part missing file")
			}
			var data message.FileData
			switch p.File.DataType {
			case "":
				// Persisted flat file data uses URL prefixes to distinguish transport.
				if strings.HasPrefix(p.File.Data, "http://") || strings.HasPrefix(p.File.Data, "https://") || strings.HasPrefix(p.File.Data, "data:") {
					data = message.FileDataURL{URL: p.File.Data}
				} else {
					data = message.FileDataBytes{Data: p.File.Data}
				}
			case "data":
				data = message.FileDataBytes{Data: p.File.Data}
			case "url":
				data = message.FileDataURL{URL: p.File.Data}
			case "text":
				data = message.FileDataText{Text: p.File.Data}
			case "reference":
				data = message.FileDataReference{Reference: p.File.Reference}
			default:
				panic("session: unknown file data type " + p.File.DataType)
			}
			m.Content.Parts = append(m.Content.Parts, message.FilePart{Data: data, MimeType: p.File.MimeType, Filename: p.File.Filename, ProviderOptions: p.ProviderOptions})
		case "tool":
			if p.Tool == nil {
				panic("session: tool part missing tool")
			}
			if p.Tool.Result || msg.Role == "tool" {
				m.Content.Parts = append(m.Content.Parts, message.ToolResultPart{
					ToolCallID: p.Tool.CallID, ToolName: p.Tool.Name, Output: toolOutputFromSession(p.Tool),
					ProviderExecuted: p.Tool.ProviderExecuted, ProviderOptions: p.ProviderOptions,
				})
			} else {
				m.Content.Parts = append(m.Content.Parts, message.ToolCallPart{
					ID: p.Tool.CallID, Name: p.Tool.Name, Input: json.RawMessage(p.Tool.Input),
					ProviderExecuted: p.Tool.ProviderExecuted, ProviderOptions: p.ProviderOptions,
				})
			}
		case "tool-approval-request":
			if p.ApprovalRequest == nil {
				panic("session: missing approval request")
			}
			m.Content.Parts = append(m.Content.Parts, *p.ApprovalRequest)
		case "tool-approval-response":
			if p.ApprovalResponse == nil {
				panic("session: missing approval response")
			}
			m.Content.Parts = append(m.Content.Parts, *p.ApprovalResponse)
		case "compaction":
			m.Content.Parts = append(m.Content.Parts, message.TextPart{Text: CompactionTriggerPrompt})
		default:
			panic("session: unknown part type " + p.Type)
		}
	}
	return []goai.Message{m}
}

func toolOutputFromSession(p *ToolPart) message.ToolResultOutput {
	kind, output := p.OutputType, p.Output
	if p.Compacted {
		if kind == "" {
			output = DefaultPrunedMessage(PrunedInfo{Type: "tool_output"})
		}
	}
	if kind == "" {
		switch p.Outcome {
		case "error":
			kind = "error-text"
		case "denied":
			kind = "execution-denied"
		default:
			kind = "text"
		}
	}
	switch kind {
	case "text":
		return message.TextOutput{Value: output, ProviderOptions: p.OutputProviderOptions}
	case "error-text":
		return message.ErrorTextOutput{Value: output, ProviderOptions: p.OutputProviderOptions}
	case "execution-denied":
		return message.ExecutionDeniedOutput{Reason: output, ProviderOptions: p.OutputProviderOptions}
	case "json", "error-json":
		if !json.Valid([]byte(output)) {
			panic("session: invalid JSON tool output")
		}
		if kind == "json" {
			return message.JSONOutput{Value: json.RawMessage(output), ProviderOptions: p.OutputProviderOptions}
		}
		return message.ErrorJSONOutput{Value: json.RawMessage(output), ProviderOptions: p.OutputProviderOptions}
	case "content":
		var items []message.ToolContentItem
		if err := json.Unmarshal([]byte(output), &items); err != nil {
			panic(fmt.Errorf("session: content output: %w", err))
		}
		return message.ContentOutput{Value: items}
	default:
		panic("session: unknown output type " + kind)
	}
}
