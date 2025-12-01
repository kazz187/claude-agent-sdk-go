package claude

import (
	"testing"
)

func TestParseMessage_UserMessage(t *testing.T) {
	data := RawMessage{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": "Hello, Claude!",
		},
		"parent_tool_use_id": nil,
	}

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	userMsg, ok := msg.(*UserMessage)
	if !ok {
		t.Fatalf("expected *UserMessage, got %T", msg)
	}

	if userMsg.GetContentString() != "Hello, Claude!" {
		t.Errorf("expected content 'Hello, Claude!', got '%s'", userMsg.GetContentString())
	}
}

func TestParseMessage_AssistantMessage(t *testing.T) {
	data := RawMessage{
		"type": "assistant",
		"message": map[string]any{
			"model": "claude-3-opus",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Hello! How can I help you?",
				},
			},
		},
	}

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assistantMsg, ok := msg.(*AssistantMessage)
	if !ok {
		t.Fatalf("expected *AssistantMessage, got %T", msg)
	}

	if assistantMsg.Model != "claude-3-opus" {
		t.Errorf("expected model 'claude-3-opus', got '%s'", assistantMsg.Model)
	}

	if len(assistantMsg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(assistantMsg.Content))
	}

	textBlock, ok := assistantMsg.Content[0].(TextBlock)
	if !ok {
		t.Fatalf("expected TextBlock, got %T", assistantMsg.Content[0])
	}

	if textBlock.Text != "Hello! How can I help you?" {
		t.Errorf("expected text 'Hello! How can I help you?', got '%s'", textBlock.Text)
	}
}

func TestParseMessage_ToolUseBlock(t *testing.T) {
	data := RawMessage{
		"type": "assistant",
		"message": map[string]any{
			"model": "claude-3-opus",
			"content": []any{
				map[string]any{
					"type": "tool_use",
					"id":   "tool_123",
					"name": "Bash",
					"input": map[string]any{
						"command": "echo hello",
					},
				},
			},
		},
	}

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assistantMsg, ok := msg.(*AssistantMessage)
	if !ok {
		t.Fatalf("expected *AssistantMessage, got %T", msg)
	}

	if len(assistantMsg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(assistantMsg.Content))
	}

	toolBlock, ok := assistantMsg.Content[0].(ToolUseBlock)
	if !ok {
		t.Fatalf("expected ToolUseBlock, got %T", assistantMsg.Content[0])
	}

	if toolBlock.ID != "tool_123" {
		t.Errorf("expected ID 'tool_123', got '%s'", toolBlock.ID)
	}

	if toolBlock.Name != "Bash" {
		t.Errorf("expected Name 'Bash', got '%s'", toolBlock.Name)
	}

	if cmd, ok := toolBlock.Input["command"].(string); !ok || cmd != "echo hello" {
		t.Errorf("expected command 'echo hello', got '%v'", toolBlock.Input["command"])
	}
}

func TestParseMessage_ResultMessage(t *testing.T) {
	cost := 0.0123
	data := RawMessage{
		"type":            "result",
		"subtype":         "success",
		"duration_ms":     float64(1500),
		"duration_api_ms": float64(1200),
		"is_error":        false,
		"num_turns":       float64(3),
		"session_id":      "session_abc123",
		"total_cost_usd":  cost,
	}

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resultMsg, ok := msg.(*ResultMessage)
	if !ok {
		t.Fatalf("expected *ResultMessage, got %T", msg)
	}

	if resultMsg.Subtype != "success" {
		t.Errorf("expected subtype 'success', got '%s'", resultMsg.Subtype)
	}

	if resultMsg.DurationMs != 1500 {
		t.Errorf("expected duration_ms 1500, got %d", resultMsg.DurationMs)
	}

	if resultMsg.SessionID != "session_abc123" {
		t.Errorf("expected session_id 'session_abc123', got '%s'", resultMsg.SessionID)
	}

	if resultMsg.TotalCostUSD == nil || *resultMsg.TotalCostUSD != cost {
		t.Errorf("expected total_cost_usd %.4f, got %v", cost, resultMsg.TotalCostUSD)
	}
}

func TestParseMessage_SystemMessage(t *testing.T) {
	data := RawMessage{
		"type":    "system",
		"subtype": "init",
		"data":    map[string]any{"version": "1.0.0"},
	}

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	systemMsg, ok := msg.(*SystemMessage)
	if !ok {
		t.Fatalf("expected *SystemMessage, got %T", msg)
	}

	if systemMsg.Subtype != "init" {
		t.Errorf("expected subtype 'init', got '%s'", systemMsg.Subtype)
	}
}

func TestParseMessage_InvalidType(t *testing.T) {
	data := RawMessage{
		"type": "unknown_type",
	}

	_, err := ParseMessage(data)
	if err == nil {
		t.Fatal("expected error for unknown message type")
	}
}

func TestParseMessage_MissingType(t *testing.T) {
	data := RawMessage{
		"content": "no type field",
	}

	_, err := ParseMessage(data)
	if err == nil {
		t.Fatal("expected error for missing type field")
	}
}
