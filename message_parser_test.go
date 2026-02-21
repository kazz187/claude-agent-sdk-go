package claudeagent

import (
	"fmt"
	"os"
	"strings"
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

func TestParseMessage_UserMessage_UUID(t *testing.T) {
	data := RawMessage{
		"type": "user",
		"uuid": "test-uuid-123",
		"message": map[string]any{
			"role":    "user",
			"content": "Hello!",
		},
	}

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	userMsg, ok := msg.(*UserMessage)
	if !ok {
		t.Fatalf("expected *UserMessage, got %T", msg)
	}

	if userMsg.UUID != "test-uuid-123" {
		t.Errorf("expected UUID 'test-uuid-123', got '%s'", userMsg.UUID)
	}
}

func TestParseMessage_UserMessage_ToolUseResult(t *testing.T) {
	data := RawMessage{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": "result",
		},
		"tool_use_result": map[string]any{
			"output": "some output",
		},
	}

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	userMsg, ok := msg.(*UserMessage)
	if !ok {
		t.Fatalf("expected *UserMessage, got %T", msg)
	}

	if userMsg.ToolUseResult == nil {
		t.Fatal("expected ToolUseResult to be set")
	}

	if userMsg.ToolUseResult["output"] != "some output" {
		t.Errorf("expected ToolUseResult output 'some output', got '%v'", userMsg.ToolUseResult["output"])
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

func TestParseMessage_AssistantMessage_Error(t *testing.T) {
	data := RawMessage{
		"type":  "assistant",
		"error": "rate_limit",
		"message": map[string]any{
			"model": "claude-3-opus",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Rate limited",
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

	if assistantMsg.Error != AssistantMessageErrorRateLimit {
		t.Errorf("expected error 'rate_limit', got '%s'", assistantMsg.Error)
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

func TestParseMessage_UnknownType_ReturnsNil(t *testing.T) {
	data := RawMessage{
		"type": "unknown_type",
	}

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("expected no error for unknown type, got: %v", err)
	}
	if msg != nil {
		t.Fatalf("expected nil message for unknown type, got: %v", msg)
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

// ThinkingConfig tests

func TestThinkingConfigAdaptive(t *testing.T) {
	var cfg ThinkingConfig = ThinkingConfigAdaptive{}
	if cfg.GetType() != "adaptive" {
		t.Errorf("expected type 'adaptive', got '%s'", cfg.GetType())
	}
}

func TestThinkingConfigEnabled(t *testing.T) {
	var cfg ThinkingConfig = ThinkingConfigEnabled{BudgetTokens: 10000}
	if cfg.GetType() != "enabled" {
		t.Errorf("expected type 'enabled', got '%s'", cfg.GetType())
	}
	if tc, ok := cfg.(ThinkingConfigEnabled); ok {
		if tc.BudgetTokens != 10000 {
			t.Errorf("expected BudgetTokens 10000, got %d", tc.BudgetTokens)
		}
	}
}

func TestThinkingConfigDisabled(t *testing.T) {
	var cfg ThinkingConfig = ThinkingConfigDisabled{}
	if cfg.GetType() != "disabled" {
		t.Errorf("expected type 'disabled', got '%s'", cfg.GetType())
	}
}

// ThinkingConfig resolution tests

func TestThinkingConfigResolution_Adaptive(t *testing.T) {
	// Adaptive with no MaxThinkingTokens → 32000
	opts := ClaudeAgentOptions{
		Thinking: ThinkingConfigAdaptive{},
	}

	resolved := resolveThinkingTokens(opts)
	if resolved == nil || *resolved != 32000 {
		t.Errorf("expected 32000 for adaptive, got %v", resolved)
	}
}

func TestThinkingConfigResolution_AdaptiveWithExisting(t *testing.T) {
	// Adaptive with existing MaxThinkingTokens → use existing
	existing := 50000
	opts := ClaudeAgentOptions{
		MaxThinkingTokens: &existing,
		Thinking:          ThinkingConfigAdaptive{},
	}

	resolved := resolveThinkingTokens(opts)
	if resolved == nil || *resolved != 50000 {
		t.Errorf("expected 50000 for adaptive with existing, got %v", resolved)
	}
}

func TestThinkingConfigResolution_Enabled(t *testing.T) {
	opts := ClaudeAgentOptions{
		Thinking: ThinkingConfigEnabled{BudgetTokens: 20000},
	}

	resolved := resolveThinkingTokens(opts)
	if resolved == nil || *resolved != 20000 {
		t.Errorf("expected 20000 for enabled, got %v", resolved)
	}
}

func TestThinkingConfigResolution_Disabled(t *testing.T) {
	opts := ClaudeAgentOptions{
		Thinking: ThinkingConfigDisabled{},
	}

	resolved := resolveThinkingTokens(opts)
	if resolved == nil || *resolved != 0 {
		t.Errorf("expected 0 for disabled, got %v", resolved)
	}
}

func TestThinkingConfigResolution_NilThinking(t *testing.T) {
	existing := 8000
	opts := ClaudeAgentOptions{
		MaxThinkingTokens: &existing,
	}

	resolved := resolveThinkingTokens(opts)
	if resolved == nil || *resolved != 8000 {
		t.Errorf("expected 8000 for nil thinking, got %v", resolved)
	}
}

// CLI flag generation tests

// createFakeCLI creates a temporary file to use as a fake CLI path in tests.
func createFakeCLI(t *testing.T) string {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "claude-test-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })
	return tmpFile.Name()
}

func TestBuildCommand_ToolsStringSlice(t *testing.T) {
	cliPath := createFakeCLI(t)
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			Tools:   []string{"Read", "Write"},
			CLIPath: cliPath,
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmdStr := strings.Join(cmd, " ")
	if !strings.Contains(cmdStr, "--tools Read,Write") {
		t.Errorf("expected --tools Read,Write in command, got: %s", cmdStr)
	}
}

func TestBuildCommand_ToolsEmptySlice(t *testing.T) {
	cliPath := createFakeCLI(t)
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			Tools:   []string{},
			CLIPath: cliPath,
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmdStr := strings.Join(cmd, " ")
	// Should have --tools with empty value
	found := false
	for i, arg := range cmd {
		if arg == "--tools" && i+1 < len(cmd) && cmd[i+1] == "" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --tools '' in command, got: %s", cmdStr)
	}
}

func TestBuildCommand_ToolsPreset(t *testing.T) {
	cliPath := createFakeCLI(t)
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			Tools:   &ToolsPreset{Type: "preset", Preset: "claude_code"},
			CLIPath: cliPath,
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmdStr := strings.Join(cmd, " ")
	if !strings.Contains(cmdStr, "--tools default") {
		t.Errorf("expected --tools default in command, got: %s", cmdStr)
	}
}

func TestBuildCommand_Betas(t *testing.T) {
	cliPath := createFakeCLI(t)
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			Betas:   []SdkBeta{SdkBetaContext1M},
			CLIPath: cliPath,
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmdStr := strings.Join(cmd, " ")
	if !strings.Contains(cmdStr, "--betas context-1m-2025-08-07") {
		t.Errorf("expected --betas flag in command, got: %s", cmdStr)
	}
}

func TestBuildCommand_Effort(t *testing.T) {
	cliPath := createFakeCLI(t)
	effort := "high"
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			Effort:  &effort,
			CLIPath: cliPath,
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmdStr := strings.Join(cmd, " ")
	if !strings.Contains(cmdStr, "--effort high") {
		t.Errorf("expected --effort high in command, got: %s", cmdStr)
	}
}

func TestBuildCommand_MaxBudgetFormat(t *testing.T) {
	cliPath := createFakeCLI(t)
	budget := 1.5
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			MaxBudgetUSD: &budget,
			CLIPath:      cliPath,
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the --max-budget-usd value
	for i, arg := range cmd {
		if arg == "--max-budget-usd" && i+1 < len(cmd) {
			val := cmd[i+1]
			if val != "1.5" {
				t.Errorf("expected max-budget-usd '1.5', got '%s'", val)
			}
			return
		}
	}
	t.Error("--max-budget-usd flag not found in command")
}

func TestBuildCommand_MaxBudgetFormatInteger(t *testing.T) {
	cliPath := createFakeCLI(t)
	budget := 10.0
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			MaxBudgetUSD: &budget,
			CLIPath:      cliPath,
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, arg := range cmd {
		if arg == "--max-budget-usd" && i+1 < len(cmd) {
			val := cmd[i+1]
			if val != "10" {
				t.Errorf("expected max-budget-usd '10' (no trailing zeros), got '%s'", val)
			}
			return
		}
	}
	t.Error("--max-budget-usd flag not found in command")
}

func TestBuildCommand_AlwaysStreaming(t *testing.T) {
	cliPath := createFakeCLI(t)
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			CLIPath: cliPath,
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmdStr := strings.Join(cmd, " ")
	if !strings.Contains(cmdStr, "--input-format stream-json") {
		t.Errorf("expected --input-format stream-json in command, got: %s", cmdStr)
	}
	if strings.Contains(cmdStr, "--print") {
		t.Errorf("should not contain --print flag, got: %s", cmdStr)
	}
}

func TestBuildCommand_NoAgentsFlag(t *testing.T) {
	cliPath := createFakeCLI(t)
	transport := &SubprocessTransport{
		options: ClaudeAgentOptions{
			CLIPath: cliPath,
			Agents: map[string]AgentDefinition{
				"test": {Description: "test", Prompt: "test"},
			},
		},
		isStreaming:   true,
		maxBufferSize: defaultMaxBufferSize,
	}

	cmd, err := transport.buildCommand()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmdStr := strings.Join(cmd, " ")
	if strings.Contains(cmdStr, "--agents") {
		t.Errorf("should not contain --agents flag (agents sent via initialize), got: %s", cmdStr)
	}
}

// Error hierarchy tests

func TestCLINotFoundError_TypeHierarchy(t *testing.T) {
	err := NewCLINotFoundError("not found")

	// CLINotFoundError should embed CLIConnectionError (structural embedding)
	// Access the embedded CLIConnectionError field
	connErr := err.CLIConnectionError
	if connErr.Message != "not found" {
		t.Errorf("expected message 'not found' via CLIConnectionError, got '%s'", connErr.Message)
	}

	// CLIConnectionError should embed ClaudeSDKError
	sdkErr := connErr.ClaudeSDKError
	if sdkErr.Message != "not found" {
		t.Errorf("expected message 'not found' via ClaudeSDKError, got '%s'", sdkErr.Message)
	}

	// Error() method should work
	if err.Error() != "not found" {
		t.Errorf("expected Error() 'not found', got '%s'", err.Error())
	}
}

func TestProcessError_ErrorMessage_WithStderr(t *testing.T) {
	err := NewProcessError("command failed", 1, "some error output")
	errMsg := err.Error()

	if !strings.Contains(errMsg, "exit code: 1") {
		t.Errorf("expected 'exit code: 1' in error message, got: %s", errMsg)
	}

	if !strings.Contains(errMsg, "some error output") {
		t.Errorf("expected stderr in error message, got: %s", errMsg)
	}
}

func TestProcessError_ErrorMessage_WithoutStderr(t *testing.T) {
	err := NewProcessError("command failed", 1, "")
	errMsg := err.Error()

	if !strings.Contains(errMsg, "exit code: 1") {
		t.Errorf("expected 'exit code: 1' in error message, got: %s", errMsg)
	}

	if strings.Contains(errMsg, "Error output") {
		t.Errorf("should not contain 'Error output' when stderr is empty, got: %s", errMsg)
	}
}

// SDK MCP Server tests

func TestSdkMcpServer_AddAndFindTool(t *testing.T) {
	server := NewSdkMcpServer("test-server", "1.0.0")
	server.AddTool(SdkMcpTool{
		Name:        "test-tool",
		Description: "A test tool",
		Handler: func(args map[string]any) ([]map[string]any, error) {
			return []map[string]any{{"type": "text", "text": "result"}}, nil
		},
	})

	tools := server.GetTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	tool := server.FindTool("test-tool")
	if tool == nil {
		t.Fatal("expected to find tool 'test-tool'")
	}

	if tool.Name != "test-tool" {
		t.Errorf("expected tool name 'test-tool', got '%s'", tool.Name)
	}

	notFound := server.FindTool("nonexistent")
	if notFound != nil {
		t.Error("expected nil for nonexistent tool")
	}
}

// Helper for tests - resolves thinking tokens the same way as buildCommand
func resolveThinkingTokens(opts ClaudeAgentOptions) *int {
	resolved := opts.MaxThinkingTokens
	if opts.Thinking != nil {
		switch tc := opts.Thinking.(type) {
		case ThinkingConfigAdaptive:
			if resolved == nil {
				v := 32000
				resolved = &v
			}
		case ThinkingConfigEnabled:
			resolved = &tc.BudgetTokens
		case ThinkingConfigDisabled:
			v := 0
			resolved = &v
		}
	}
	return resolved
}

// Verify NewSubprocessTransport always creates streaming transport
func TestNewSubprocessTransport_AlwaysStreaming(t *testing.T) {
	transport := NewSubprocessTransport("some prompt", ClaudeAgentOptions{})
	if !transport.isStreaming {
		t.Error("expected isStreaming to be true regardless of prompt")
	}

	transport2 := NewSubprocessTransport("", ClaudeAgentOptions{})
	if !transport2.isStreaming {
		t.Error("expected isStreaming to be true for empty prompt")
	}
}

// Test formatBudget helper
func TestFormatBudget(t *testing.T) {
	tests := []struct {
		input    float64
		expected string
	}{
		{1.5, "1.5"},
		{10.0, "10"},
		{0.5, "0.5"},
		{100.0, "100"},
		{1.23, "1.23"},
		{0.001, "0.001"},
	}

	for _, tt := range tests {
		result := formatBudget(tt.input)
		if result != tt.expected {
			t.Errorf("formatBudget(%v) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

// SDK MCP JSONRPC handling tests

func TestHandleSdkMcpRequest_Initialize(t *testing.T) {
	server := NewSdkMcpServer("test-server", "1.0.0")
	q := &Query{
		sdkMcpServers: map[string]*SdkMcpServer{
			"test-server": server,
		},
	}

	response := q.handleSdkMcpRequest("test-server", map[string]any{
		"method": "initialize",
		"id":     1,
	})

	if response["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc '2.0', got '%v'", response["jsonrpc"])
	}

	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatal("expected result in response")
	}

	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatal("expected serverInfo in result")
	}

	if serverInfo["name"] != "test-server" {
		t.Errorf("expected server name 'test-server', got '%v'", serverInfo["name"])
	}
}

func TestHandleSdkMcpRequest_ToolsList(t *testing.T) {
	server := NewSdkMcpServer("test-server", "1.0.0")
	server.AddTool(SdkMcpTool{
		Name:        "my-tool",
		Description: "My test tool",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"arg": map[string]any{"type": "string"},
			},
		},
	})

	q := &Query{
		sdkMcpServers: map[string]*SdkMcpServer{
			"test-server": server,
		},
	}

	response := q.handleSdkMcpRequest("test-server", map[string]any{
		"method": "tools/list",
		"id":     2,
	})

	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatal("expected result in response")
	}

	tools, ok := result["tools"].([]map[string]any)
	if !ok {
		t.Fatal("expected tools array in result")
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	if tools[0]["name"] != "my-tool" {
		t.Errorf("expected tool name 'my-tool', got '%v'", tools[0]["name"])
	}
}

func TestHandleSdkMcpRequest_ToolsCall(t *testing.T) {
	server := NewSdkMcpServer("test-server", "1.0.0")
	server.AddTool(SdkMcpTool{
		Name: "greet",
		Handler: func(args map[string]any) ([]map[string]any, error) {
			name := fmt.Sprintf("Hello, %v!", args["name"])
			return []map[string]any{{"type": "text", "text": name}}, nil
		},
	})

	q := &Query{
		sdkMcpServers: map[string]*SdkMcpServer{
			"test-server": server,
		},
	}

	response := q.handleSdkMcpRequest("test-server", map[string]any{
		"method": "tools/call",
		"id":     3,
		"params": map[string]any{
			"name":      "greet",
			"arguments": map[string]any{"name": "World"},
		},
	})

	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatal("expected result in response")
	}

	content, ok := result["content"].([]map[string]any)
	if !ok {
		t.Fatal("expected content in result")
	}

	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}

	if content[0]["text"] != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got '%v'", content[0]["text"])
	}
}

func TestHandleSdkMcpRequest_ServerNotFound(t *testing.T) {
	q := &Query{
		sdkMcpServers: map[string]*SdkMcpServer{},
	}

	response := q.handleSdkMcpRequest("nonexistent", map[string]any{
		"method": "initialize",
		"id":     1,
	})

	if response["error"] == nil {
		t.Error("expected error for nonexistent server")
	}
}
