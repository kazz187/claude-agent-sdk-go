package claude

import (
	"fmt"
)

// ParseMessage parses a raw message from CLI output into a typed Message.
func ParseMessage(data RawMessage) (Message, error) {
	if data == nil {
		return nil, NewMessageParseError("message data is nil", data)
	}

	msgType, ok := data["type"].(string)
	if !ok {
		return nil, NewMessageParseError("message missing 'type' field", data)
	}

	switch msgType {
	case "user":
		return parseUserMessage(data)
	case "assistant":
		return parseAssistantMessage(data)
	case "system":
		return parseSystemMessage(data)
	case "result":
		return parseResultMessage(data)
	case "stream_event":
		return parseStreamEvent(data)
	default:
		return nil, NewMessageParseError(fmt.Sprintf("unknown message type: %s", msgType), data)
	}
}

func parseUserMessage(data RawMessage) (*UserMessage, error) {
	msg := &UserMessage{}

	// Get parent_tool_use_id
	if parentID, ok := data["parent_tool_use_id"].(string); ok {
		msg.ParentToolUseID = parentID
	}

	// Get message content
	messageData, ok := data["message"].(map[string]any)
	if !ok {
		return nil, NewMessageParseError("user message missing 'message' field", data)
	}

	content := messageData["content"]
	switch c := content.(type) {
	case string:
		msg.Content = c
	case []any:
		blocks, err := parseContentBlocks(c)
		if err != nil {
			return nil, err
		}
		msg.Content = blocks
	default:
		msg.Content = content
	}

	return msg, nil
}

func parseAssistantMessage(data RawMessage) (*AssistantMessage, error) {
	msg := &AssistantMessage{}

	// Get parent_tool_use_id
	if parentID, ok := data["parent_tool_use_id"].(string); ok {
		msg.ParentToolUseID = parentID
	}

	// Get message data
	messageData, ok := data["message"].(map[string]any)
	if !ok {
		return nil, NewMessageParseError("assistant message missing 'message' field", data)
	}

	// Get model
	if model, ok := messageData["model"].(string); ok {
		msg.Model = model
	}

	// Get content blocks
	contentArr, ok := messageData["content"].([]any)
	if !ok {
		return nil, NewMessageParseError("assistant message missing 'content' field", data)
	}

	blocks, err := parseContentBlocks(contentArr)
	if err != nil {
		return nil, err
	}
	msg.Content = blocks

	return msg, nil
}

func parseSystemMessage(data RawMessage) (*SystemMessage, error) {
	msg := &SystemMessage{
		Data: data,
	}

	if subtype, ok := data["subtype"].(string); ok {
		msg.Subtype = subtype
	} else {
		return nil, NewMessageParseError("system message missing 'subtype' field", data)
	}

	return msg, nil
}

func parseResultMessage(data RawMessage) (*ResultMessage, error) {
	msg := &ResultMessage{}

	if subtype, ok := data["subtype"].(string); ok {
		msg.Subtype = subtype
	} else {
		return nil, NewMessageParseError("result message missing 'subtype' field", data)
	}

	if durationMs, ok := data["duration_ms"].(float64); ok {
		msg.DurationMs = int(durationMs)
	}

	if durationAPIMs, ok := data["duration_api_ms"].(float64); ok {
		msg.DurationAPIMs = int(durationAPIMs)
	}

	if isError, ok := data["is_error"].(bool); ok {
		msg.IsError = isError
	}

	if numTurns, ok := data["num_turns"].(float64); ok {
		msg.NumTurns = int(numTurns)
	}

	if sessionID, ok := data["session_id"].(string); ok {
		msg.SessionID = sessionID
	} else {
		return nil, NewMessageParseError("result message missing 'session_id' field", data)
	}

	if totalCost, ok := data["total_cost_usd"].(float64); ok {
		msg.TotalCostUSD = &totalCost
	}

	if usage, ok := data["usage"].(map[string]any); ok {
		msg.Usage = usage
	}

	if result, ok := data["result"].(string); ok {
		msg.Result = result
	}

	if structuredOutput, ok := data["structured_output"]; ok {
		msg.StructuredOutput = structuredOutput
	}

	return msg, nil
}

func parseStreamEvent(data RawMessage) (*StreamEvent, error) {
	msg := &StreamEvent{}

	if uuid, ok := data["uuid"].(string); ok {
		msg.UUID = uuid
	} else {
		return nil, NewMessageParseError("stream_event message missing 'uuid' field", data)
	}

	if sessionID, ok := data["session_id"].(string); ok {
		msg.SessionID = sessionID
	} else {
		return nil, NewMessageParseError("stream_event message missing 'session_id' field", data)
	}

	if event, ok := data["event"].(map[string]any); ok {
		msg.Event = event
	} else {
		return nil, NewMessageParseError("stream_event message missing 'event' field", data)
	}

	if parentID, ok := data["parent_tool_use_id"].(string); ok {
		msg.ParentToolUseID = parentID
	}

	return msg, nil
}

func parseContentBlocks(blocks []any) ([]ContentBlock, error) {
	result := make([]ContentBlock, 0, len(blocks))

	for _, block := range blocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}

		blockType, ok := blockMap["type"].(string)
		if !ok {
			continue
		}

		switch blockType {
		case "text":
			text, _ := blockMap["text"].(string)
			result = append(result, TextBlock{Text: text})

		case "thinking":
			thinking, _ := blockMap["thinking"].(string)
			signature, _ := blockMap["signature"].(string)
			result = append(result, ThinkingBlock{
				Thinking:  thinking,
				Signature: signature,
			})

		case "tool_use":
			id, _ := blockMap["id"].(string)
			name, _ := blockMap["name"].(string)
			input, _ := blockMap["input"].(map[string]any)
			result = append(result, ToolUseBlock{
				ID:    id,
				Name:  name,
				Input: input,
			})

		case "tool_result":
			toolUseID, _ := blockMap["tool_use_id"].(string)
			content := blockMap["content"]
			var isError *bool
			if ie, ok := blockMap["is_error"].(bool); ok {
				isError = &ie
			}
			result = append(result, ToolResultBlock{
				ToolUseID: toolUseID,
				Content:   content,
				IsError:   isError,
			})
		}
	}

	return result, nil
}
