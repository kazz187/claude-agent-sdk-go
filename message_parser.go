package claudeagent

import (
	"encoding/json"
)

// Wire format structs for parsing CLI messages.

// userMessageWire is the wire format for user messages from the CLI.
type userMessageWire struct {
	UUID            string         `json:"uuid,omitempty"`
	ParentToolUseID string         `json:"parent_tool_use_id,omitempty"`
	ToolUseResult   map[string]any `json:"tool_use_result,omitempty"`
	Message         struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// assistantMessageWire is the wire format for assistant messages from the CLI.
type assistantMessageWire struct {
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
	Error           string `json:"error,omitempty"`
	Message         struct {
		Model   string          `json:"model,omitempty"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// systemMessageWire is the wire format for system messages from the CLI.
type systemMessageWire struct {
	Subtype string `json:"subtype"`
}

// streamEventWire is the wire format for stream_event messages from the CLI.
type streamEventWire struct {
	UUID            string         `json:"uuid"`
	SessionID       string         `json:"session_id"`
	Event           map[string]any `json:"event"`
	ParentToolUseID string         `json:"parent_tool_use_id,omitempty"`
}

// contentBlockWire is a unified wire format for all content block types.
type contentBlockWire struct {
	Type      ContentBlockType `json:"type"`
	Text      string           `json:"text,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	Signature string           `json:"signature,omitempty"`
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Input     map[string]any   `json:"input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	Content   any              `json:"content,omitempty"`
	IsError   *bool            `json:"is_error,omitempty"`
	Source    *ImageSource     `json:"source,omitempty"`
}

// ParseMessage parses a raw JSON message from CLI output into a typed Message.
// Returns nil, nil for unknown message types (forward-compatible).
func ParseMessage(data json.RawMessage) (Message, error) {
	if len(data) == 0 {
		return nil, NewMessageParseError("message data is nil", data)
	}

	var envelope messageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, NewMessageParseError("failed to parse message envelope", data)
	}

	if envelope.Type == "" {
		return nil, NewMessageParseError("message missing 'type' field", data)
	}

	switch envelope.Type {
	case MessageTypeUser:
		return parseUserMessage(data)
	case MessageTypeAssistant:
		return parseAssistantMessage(data)
	case MessageTypeSystem:
		return parseSystemMessage(data)
	case MessageTypeResult:
		return parseResultMessage(data)
	case MessageTypeStreamEvent:
		return parseStreamEvent(data)
	case MessageTypeRateLimitEvent:
		return parseRateLimitEvent(data)
	default:
		// Forward-compatible: skip unrecognized message types so newer
		// CLI versions don't crash older SDK versions.
		return nil, nil
	}
}

func parseRateLimitEvent(data json.RawMessage) (*RateLimitEvent, error) {
	var wire struct {
		RateLimitInfo map[string]any `json:"rate_limit_info"`
		UUID          string         `json:"uuid"`
		SessionID     string         `json:"session_id"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, NewMessageParseError("failed to parse rate_limit_event message", data)
	}
	if wire.RateLimitInfo == nil {
		return nil, NewMessageParseError("rate_limit_event missing 'rate_limit_info' field", data)
	}

	info := RateLimitInfo{Raw: wire.RateLimitInfo}
	if s, ok := wire.RateLimitInfo["status"].(string); ok {
		info.Status = s
	}
	if v, ok := wire.RateLimitInfo["resetsAt"].(float64); ok {
		n := int64(v)
		info.ResetsAt = &n
	}
	if s, ok := wire.RateLimitInfo["rateLimitType"].(string); ok {
		info.RateLimitType = s
	}
	if v, ok := wire.RateLimitInfo["utilization"].(float64); ok {
		info.Utilization = &v
	}
	if s, ok := wire.RateLimitInfo["overageStatus"].(string); ok {
		info.OverageStatus = s
	}
	if v, ok := wire.RateLimitInfo["overageResetsAt"].(float64); ok {
		n := int64(v)
		info.OverageResetsAt = &n
	}
	if s, ok := wire.RateLimitInfo["overageDisabledReason"].(string); ok {
		info.OverageDisabledReason = s
	}

	return &RateLimitEvent{
		RateLimitInfo: info,
		UUID:          wire.UUID,
		SessionID:     wire.SessionID,
	}, nil
}

func parseUserMessage(data json.RawMessage) (*UserMessage, error) {
	var wire userMessageWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, NewMessageParseError("failed to parse user message", data)
	}

	if wire.Message.Content == nil {
		return nil, NewMessageParseError("user message missing 'message' field", data)
	}

	msg := &UserMessage{
		UUID:            wire.UUID,
		ParentToolUseID: wire.ParentToolUseID,
		ToolUseResult:   wire.ToolUseResult,
	}

	// Content can be a string or an array of content blocks.
	var content any
	if err := json.Unmarshal(wire.Message.Content, &content); err != nil {
		return nil, NewMessageParseError("failed to parse user message content", data)
	}

	switch c := content.(type) {
	case string:
		msg.Content = c
	case []any:
		blocks, err := parseContentBlocksFromRaw(wire.Message.Content)
		if err != nil {
			return nil, err
		}
		msg.Content = blocks
	default:
		msg.Content = content
	}

	return msg, nil
}

func parseAssistantMessage(data json.RawMessage) (*AssistantMessage, error) {
	var wire assistantMessageWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, NewMessageParseError("failed to parse assistant message", data)
	}

	if wire.Message.Content == nil {
		return nil, NewMessageParseError("assistant message missing 'content' field", data)
	}

	blocks, err := parseContentBlocksFromRaw(wire.Message.Content)
	if err != nil {
		return nil, NewMessageParseError("failed to parse assistant content blocks", data)
	}

	return &AssistantMessage{
		ParentToolUseID: wire.ParentToolUseID,
		Error:           AssistantMessageError(wire.Error),
		Model:           wire.Message.Model,
		Content:         blocks,
	}, nil
}

func parseSystemMessage(data json.RawMessage) (Message, error) {
	var wire systemMessageWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, NewMessageParseError("failed to parse system message", data)
	}

	if wire.Subtype == "" {
		return nil, NewMessageParseError("system message missing 'subtype' field", data)
	}

	// Keep the full message as Data for backward compatibility.
	var fullMap map[string]any
	if err := json.Unmarshal(data, &fullMap); err != nil {
		return nil, NewMessageParseError("failed to parse system message data", data)
	}

	base := SystemMessage{
		Subtype: wire.Subtype,
		Data:    fullMap,
	}

	switch wire.Subtype {
	case "task_started":
		msg := &TaskStartedMessage{SystemMessage: base}
		if err := json.Unmarshal(data, msg); err != nil {
			return nil, NewMessageParseError("failed to parse task_started message", data)
		}
		msg.SystemMessage = base
		return msg, nil
	case "task_progress":
		msg := &TaskProgressMessage{SystemMessage: base}
		if err := json.Unmarshal(data, msg); err != nil {
			return nil, NewMessageParseError("failed to parse task_progress message", data)
		}
		msg.SystemMessage = base
		return msg, nil
	case "task_notification":
		msg := &TaskNotificationMessage{SystemMessage: base}
		if err := json.Unmarshal(data, msg); err != nil {
			return nil, NewMessageParseError("failed to parse task_notification message", data)
		}
		msg.SystemMessage = base
		return msg, nil
	}

	return &base, nil
}

func parseResultMessage(data json.RawMessage) (*ResultMessage, error) {
	msg := &ResultMessage{}
	if err := json.Unmarshal(data, msg); err != nil {
		return nil, NewMessageParseError("failed to parse result message", data)
	}
	if msg.Subtype == "" {
		return nil, NewMessageParseError("result message missing 'subtype' field", data)
	}
	if msg.SessionID == "" {
		return nil, NewMessageParseError("result message missing 'session_id' field", data)
	}
	return msg, nil
}

func parseStreamEvent(data json.RawMessage) (*StreamEvent, error) {
	var wire streamEventWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, NewMessageParseError("failed to parse stream_event message", data)
	}

	if wire.UUID == "" {
		return nil, NewMessageParseError("stream_event message missing 'uuid' field", data)
	}
	if wire.SessionID == "" {
		return nil, NewMessageParseError("stream_event message missing 'session_id' field", data)
	}
	if wire.Event == nil {
		return nil, NewMessageParseError("stream_event message missing 'event' field", data)
	}

	return &StreamEvent{
		UUID:            wire.UUID,
		SessionID:       wire.SessionID,
		Event:           wire.Event,
		ParentToolUseID: wire.ParentToolUseID,
	}, nil
}

// parseContentBlocksFromRaw parses a JSON array of content blocks.
func parseContentBlocksFromRaw(data json.RawMessage) ([]ContentBlock, error) {
	var blocks []contentBlockWire
	if err := json.Unmarshal(data, &blocks); err != nil {
		return nil, err
	}

	result := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case ContentBlockTypeText:
			result = append(result, TextBlock{Text: b.Text})
		case ContentBlockTypeThinking:
			result = append(result, ThinkingBlock{
				Thinking:  b.Thinking,
				Signature: b.Signature,
			})
		case ContentBlockTypeToolUse:
			result = append(result, ToolUseBlock{
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Input,
			})
		case ContentBlockTypeToolResult:
			result = append(result, ToolResultBlock{
				ToolUseID: b.ToolUseID,
				Content:   b.Content,
				IsError:   b.IsError,
			})
		case ContentBlockTypeImage:
			if b.Source != nil {
				result = append(result, ImageBlock{Source: *b.Source})
			}
		}
	}
	return result, nil
}
