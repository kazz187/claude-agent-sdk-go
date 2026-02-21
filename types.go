package claudeagent

import (
	"context"
	"encoding/json"
	"sync"
)

// PermissionMode represents the permission mode for Claude operations.
type PermissionMode string

const (
	PermissionModeDefault           PermissionMode = "default"
	PermissionModeAcceptEdits       PermissionMode = "acceptEdits"
	PermissionModePlan              PermissionMode = "plan"
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
)

// SdkBeta represents SDK beta feature flags.
// See https://docs.anthropic.com/en/api/beta-headers
type SdkBeta string

const (
	SdkBetaContext1M SdkBeta = "context-1m-2025-08-07"
)

// ToolsPreset represents a tools preset configuration.
type ToolsPreset struct {
	Type   string `json:"type"`   // "preset"
	Preset string `json:"preset"` // "claude_code"
}

// ThinkingConfig represents the thinking configuration interface.
type ThinkingConfig interface {
	isThinkingConfig()
	// GetType returns the type of thinking config.
	GetType() string
}

// ThinkingConfigAdaptive enables adaptive thinking.
type ThinkingConfigAdaptive struct{}

func (ThinkingConfigAdaptive) isThinkingConfig() {}
func (ThinkingConfigAdaptive) GetType() string   { return "adaptive" }

// ThinkingConfigEnabled enables thinking with a specific budget.
type ThinkingConfigEnabled struct {
	BudgetTokens int `json:"budget_tokens"`
}

func (ThinkingConfigEnabled) isThinkingConfig() {}
func (ThinkingConfigEnabled) GetType() string   { return "enabled" }

// ThinkingConfigDisabled disables thinking.
type ThinkingConfigDisabled struct{}

func (ThinkingConfigDisabled) isThinkingConfig() {}
func (ThinkingConfigDisabled) GetType() string   { return "disabled" }

// SettingSource represents the source of settings.
type SettingSource string

const (
	SettingSourceUser    SettingSource = "user"
	SettingSourceProject SettingSource = "project"
	SettingSourceLocal   SettingSource = "local"
)

// SystemPromptPreset represents a preset system prompt configuration.
type SystemPromptPreset struct {
	Type   string `json:"type"`   // "preset"
	Preset string `json:"preset"` // "claude_code"
	Append string `json:"append,omitempty"`
}

// AgentDefinition represents an agent definition configuration.
type AgentDefinition struct {
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools,omitempty"`
	Model       string   `json:"model,omitempty"` // "sonnet", "opus", "haiku", "inherit"
}

// PermissionUpdateDestination represents where permissions are updated.
type PermissionUpdateDestination string

const (
	PermissionDestinationUserSettings    PermissionUpdateDestination = "userSettings"
	PermissionDestinationProjectSettings PermissionUpdateDestination = "projectSettings"
	PermissionDestinationLocalSettings   PermissionUpdateDestination = "localSettings"
	PermissionDestinationSession         PermissionUpdateDestination = "session"
)

// PermissionBehavior represents the behavior for a permission.
type PermissionBehavior string

const (
	PermissionBehaviorAllow PermissionBehavior = "allow"
	PermissionBehaviorDeny  PermissionBehavior = "deny"
	PermissionBehaviorAsk   PermissionBehavior = "ask"
)

// PermissionRuleValue represents a permission rule value.
type PermissionRuleValue struct {
	ToolName    string `json:"toolName"`
	RuleContent string `json:"ruleContent,omitempty"`
}

// PermissionUpdateType represents the type of permission update.
type PermissionUpdateType string

const (
	PermissionUpdateAddRules          PermissionUpdateType = "addRules"
	PermissionUpdateReplaceRules      PermissionUpdateType = "replaceRules"
	PermissionUpdateRemoveRules       PermissionUpdateType = "removeRules"
	PermissionUpdateSetMode           PermissionUpdateType = "setMode"
	PermissionUpdateAddDirectories    PermissionUpdateType = "addDirectories"
	PermissionUpdateRemoveDirectories PermissionUpdateType = "removeDirectories"
)

// PermissionUpdate represents a permission update configuration.
type PermissionUpdate struct {
	Type        PermissionUpdateType        `json:"type"`
	Rules       []PermissionRuleValue       `json:"rules,omitempty"`
	Behavior    PermissionBehavior          `json:"behavior,omitempty"`
	Mode        PermissionMode              `json:"mode,omitempty"`
	Directories []string                    `json:"directories,omitempty"`
	Destination PermissionUpdateDestination `json:"destination,omitempty"`
}

// ToMap converts PermissionUpdate to a map for JSON serialization.
func (p *PermissionUpdate) ToMap() map[string]any {
	result := map[string]any{
		"type": p.Type,
	}

	if p.Destination != "" {
		result["destination"] = p.Destination
	}

	switch p.Type {
	case PermissionUpdateAddRules, PermissionUpdateReplaceRules, PermissionUpdateRemoveRules:
		if len(p.Rules) > 0 {
			rules := make([]map[string]any, len(p.Rules))
			for i, rule := range p.Rules {
				rules[i] = map[string]any{
					"toolName":    rule.ToolName,
					"ruleContent": rule.RuleContent,
				}
			}
			result["rules"] = rules
		}
		if p.Behavior != "" {
			result["behavior"] = p.Behavior
		}
	case PermissionUpdateSetMode:
		if p.Mode != "" {
			result["mode"] = p.Mode
		}
	case PermissionUpdateAddDirectories, PermissionUpdateRemoveDirectories:
		if len(p.Directories) > 0 {
			result["directories"] = p.Directories
		}
	}

	return result
}

// ToolPermissionContext provides context for tool permission callbacks.
type ToolPermissionContext struct {
	Signal      context.Context    // For cancellation
	Suggestions []PermissionUpdate // Permission suggestions from CLI
}

// PermissionResult represents the result of a permission check.
type PermissionResult interface {
	isPermissionResult()
}

// PermissionResultAllow represents an allow permission result.
type PermissionResultAllow struct {
	Behavior           string             `json:"behavior"` // "allow"
	UpdatedInput       map[string]any     `json:"updatedInput,omitempty"`
	UpdatedPermissions []PermissionUpdate `json:"updatedPermissions,omitempty"`
}

func (PermissionResultAllow) isPermissionResult() {}

// PermissionResultDeny represents a deny permission result.
type PermissionResultDeny struct {
	Behavior  string `json:"behavior"` // "deny"
	Message   string `json:"message,omitempty"`
	Interrupt bool   `json:"interrupt,omitempty"`
}

func (PermissionResultDeny) isPermissionResult() {}

// CanUseToolFunc is the callback type for tool permission requests.
type CanUseToolFunc func(toolName string, input map[string]any, ctx ToolPermissionContext) (PermissionResult, error)

// HookEvent represents hook event types.
type HookEvent string

const (
	HookEventPreToolUse        HookEvent = "PreToolUse"
	HookEventPostToolUse       HookEvent = "PostToolUse"
	HookEventPostToolUseFail   HookEvent = "PostToolUseFailure"
	HookEventUserPromptSubmit  HookEvent = "UserPromptSubmit"
	HookEventStop              HookEvent = "Stop"
	HookEventSubagentStop      HookEvent = "SubagentStop"
	HookEventPreCompact        HookEvent = "PreCompact"
	HookEventNotification      HookEvent = "Notification"
	HookEventSubagentStart     HookEvent = "SubagentStart"
	HookEventPermissionRequest HookEvent = "PermissionRequest"
)

// HookInput represents the input data for hook callbacks.
type HookInput struct {
	SessionID             string         `json:"session_id"`
	TranscriptPath        string         `json:"transcript_path"`
	Cwd                   string         `json:"cwd"`
	PermissionMode        string         `json:"permission_mode,omitempty"`
	HookEventName         HookEvent      `json:"hook_event_name"`
	ToolName              string         `json:"tool_name,omitempty"`
	ToolInput             map[string]any `json:"tool_input,omitempty"`
	ToolResponse          any            `json:"tool_response,omitempty"`
	ToolUseID             string         `json:"tool_use_id,omitempty"`
	Error                 string         `json:"error,omitempty"`
	IsInterrupt           bool           `json:"is_interrupt,omitempty"`
	Prompt                string         `json:"prompt,omitempty"`
	StopHookActive        bool           `json:"stop_hook_active,omitempty"`
	Trigger               string         `json:"trigger,omitempty"`
	CustomInstructions    string         `json:"custom_instructions,omitempty"`
	AgentID               string         `json:"agent_id,omitempty"`
	AgentTranscriptPath   string         `json:"agent_transcript_path,omitempty"`
	AgentType             string         `json:"agent_type,omitempty"`
	Message               string         `json:"message,omitempty"`
	Title                 string         `json:"title,omitempty"`
	NotificationType      string         `json:"notification_type,omitempty"`
	PermissionSuggestions []any          `json:"permission_suggestions,omitempty"`
}

// HookOutput represents the output from hook callbacks.
type HookOutput struct {
	Async              *bool          `json:"async,omitempty"`
	AsyncTimeout       *int           `json:"asyncTimeout,omitempty"`
	Continue           *bool          `json:"continue,omitempty"`
	SuppressOutput     bool           `json:"suppressOutput,omitempty"`
	StopReason         string         `json:"stopReason,omitempty"`
	Decision           string         `json:"decision,omitempty"` // "block"
	SystemMessage      string         `json:"systemMessage,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	HookSpecificOutput map[string]any `json:"hookSpecificOutput,omitempty"`
}

// HookContext provides context for hook callbacks.
type HookContext struct {
	Signal context.Context
}

// HookCallback is the callback type for hooks.
type HookCallback func(input HookInput, toolUseID string, ctx HookContext) (HookOutput, error)

// HookMatcher represents a hook matcher configuration.
type HookMatcher struct {
	Matcher string         `json:"matcher,omitempty"`
	Hooks   []HookCallback `json:"-"`
	Timeout float64        `json:"timeout,omitempty"`
}

// McpServerType represents the type of MCP server.
type McpServerType string

const (
	McpServerTypeStdio McpServerType = "stdio"
	McpServerTypeSSE   McpServerType = "sse"
	McpServerTypeHTTP  McpServerType = "http"
	McpServerTypeSDK   McpServerType = "sdk"
)

// McpServerConfig represents MCP server configuration.
type McpServerConfig struct {
	Type    McpServerType     `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Name    string            `json:"name,omitempty"`
}

// SdkPluginConfig represents SDK plugin configuration.
type SdkPluginConfig struct {
	Type string `json:"type"` // "local"
	Path string `json:"path"`
}

// SandboxNetworkConfig represents network configuration for sandbox.
type SandboxNetworkConfig struct {
	AllowUnixSockets    []string `json:"allowUnixSockets,omitempty"`
	AllowAllUnixSockets bool     `json:"allowAllUnixSockets,omitempty"`
	AllowLocalBinding   bool     `json:"allowLocalBinding,omitempty"`
	HTTPProxyPort       int      `json:"httpProxyPort,omitempty"`
	SocksProxyPort      int      `json:"socksProxyPort,omitempty"`
}

// SandboxIgnoreViolations represents violations to ignore in sandbox.
type SandboxIgnoreViolations struct {
	File    []string `json:"file,omitempty"`
	Network []string `json:"network,omitempty"`
}

// SandboxSettings represents sandbox settings configuration.
type SandboxSettings struct {
	Enabled                   bool                     `json:"enabled,omitempty"`
	AutoAllowBashIfSandboxed  bool                     `json:"autoAllowBashIfSandboxed,omitempty"`
	ExcludedCommands          []string                 `json:"excludedCommands,omitempty"`
	AllowUnsandboxedCommands  bool                     `json:"allowUnsandboxedCommands,omitempty"`
	Network                   *SandboxNetworkConfig    `json:"network,omitempty"`
	IgnoreViolations          *SandboxIgnoreViolations `json:"ignoreViolations,omitempty"`
	EnableWeakerNestedSandbox bool                     `json:"enableWeakerNestedSandbox,omitempty"`
}

// ContentBlock represents a content block in messages.
type ContentBlock interface {
	isContentBlock()
}

// TextBlock represents a text content block.
type TextBlock struct {
	Text string `json:"text"`
}

func (TextBlock) isContentBlock() {}

// ThinkingBlock represents a thinking content block.
type ThinkingBlock struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

func (ThinkingBlock) isContentBlock() {}

// ToolUseBlock represents a tool use content block.
type ToolUseBlock struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

func (ToolUseBlock) isContentBlock() {}

// ToolResultBlock represents a tool result content block.
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content,omitempty"` // string or []map[string]any
	IsError   *bool  `json:"is_error,omitempty"`
}

func (ToolResultBlock) isContentBlock() {}

// Message represents a message in the conversation.
type Message interface {
	isMessage()
}

// UserMessage represents a user message.
type UserMessage struct {
	Content         any            `json:"content"` // string or []ContentBlock
	UUID            string         `json:"uuid,omitempty"`
	ParentToolUseID string         `json:"parent_tool_use_id,omitempty"`
	ToolUseResult   map[string]any `json:"tool_use_result,omitempty"`
}

func (UserMessage) isMessage() {}

// GetContentBlocks returns content blocks if content is a slice.
func (m *UserMessage) GetContentBlocks() []ContentBlock {
	if blocks, ok := m.Content.([]ContentBlock); ok {
		return blocks
	}
	return nil
}

// GetContentString returns content as string if it is a string.
func (m *UserMessage) GetContentString() string {
	if s, ok := m.Content.(string); ok {
		return s
	}
	return ""
}

// AssistantMessageError represents error types for assistant messages.
type AssistantMessageError string

const (
	AssistantMessageErrorAuthFailed     AssistantMessageError = "authentication_failed"
	AssistantMessageErrorBilling        AssistantMessageError = "billing_error"
	AssistantMessageErrorRateLimit      AssistantMessageError = "rate_limit"
	AssistantMessageErrorInvalidRequest AssistantMessageError = "invalid_request"
	AssistantMessageErrorServer         AssistantMessageError = "server_error"
	AssistantMessageErrorUnknown        AssistantMessageError = "unknown"
)

// AssistantMessage represents an assistant message.
type AssistantMessage struct {
	Content         []ContentBlock        `json:"content"`
	Model           string                `json:"model"`
	ParentToolUseID string                `json:"parent_tool_use_id,omitempty"`
	Error           AssistantMessageError `json:"error,omitempty"`
}

func (AssistantMessage) isMessage() {}

// SystemMessage represents a system message.
type SystemMessage struct {
	Subtype string         `json:"subtype"`
	Data    map[string]any `json:"data"`
}

func (SystemMessage) isMessage() {}

// ResultMessage represents a result message with cost and usage info.
type ResultMessage struct {
	Subtype          string         `json:"subtype"`
	DurationMs       int            `json:"duration_ms"`
	DurationAPIMs    int            `json:"duration_api_ms"`
	IsError          bool           `json:"is_error"`
	NumTurns         int            `json:"num_turns"`
	SessionID        string         `json:"session_id"`
	TotalCostUSD     *float64       `json:"total_cost_usd,omitempty"`
	Usage            map[string]any `json:"usage,omitempty"`
	Result           string         `json:"result,omitempty"`
	StructuredOutput any            `json:"structured_output,omitempty"`
}

func (ResultMessage) isMessage() {}

// StreamEvent represents a stream event for partial message updates.
type StreamEvent struct {
	UUID            string         `json:"uuid"`
	SessionID       string         `json:"session_id"`
	Event           map[string]any `json:"event"`
	ParentToolUseID string         `json:"parent_tool_use_id,omitempty"`
}

func (StreamEvent) isMessage() {}

// ClaudeAgentOptions represents options for the Claude agent.
type ClaudeAgentOptions struct {
	// Tools specifies the base set of tools. Can be []string, *ToolsPreset, or nil.
	Tools                any                        `json:"-"`
	AllowedTools         []string                   `json:"allowed_tools,omitempty"`
	SystemPrompt         any                        `json:"system_prompt,omitempty"` // string or *SystemPromptPreset
	McpServers           map[string]McpServerConfig `json:"mcp_servers,omitempty"`
	McpServersPath       string                     `json:"-"` // Alternative: path to MCP config file
	PermissionMode       PermissionMode             `json:"permission_mode,omitempty"`
	ContinueConversation bool                       `json:"continue_conversation,omitempty"`
	Resume               string                     `json:"resume,omitempty"`
	MaxTurns             *int                       `json:"max_turns,omitempty"`
	MaxBudgetUSD         *float64                   `json:"max_budget_usd,omitempty"`
	DisallowedTools      []string                   `json:"disallowed_tools,omitempty"`
	Model                string                     `json:"model,omitempty"`
	FallbackModel        string                     `json:"fallback_model,omitempty"`
	// Betas specifies SDK beta features. See https://docs.anthropic.com/en/api/beta-headers
	Betas                    []SdkBeta                   `json:"-"`
	PermissionPromptToolName string                      `json:"permission_prompt_tool_name,omitempty"`
	Cwd                      string                      `json:"cwd,omitempty"`
	CLIPath                  string                      `json:"cli_path,omitempty"`
	Settings                 string                      `json:"settings,omitempty"`
	AddDirs                  []string                    `json:"add_dirs,omitempty"`
	Env                      map[string]string           `json:"env,omitempty"`
	ExtraArgs                map[string]*string          `json:"extra_args,omitempty"`
	MaxBufferSize            *int                        `json:"max_buffer_size,omitempty"`
	StderrCallback           func(string)                `json:"-"`
	CanUseTool               CanUseToolFunc              `json:"-"`
	Hooks                    map[HookEvent][]HookMatcher `json:"-"`
	User                     string                      `json:"user,omitempty"`
	IncludePartialMessages   bool                        `json:"include_partial_messages,omitempty"`
	ForkSession              bool                        `json:"fork_session,omitempty"`
	Agents                   map[string]AgentDefinition  `json:"agents,omitempty"`
	SettingSources           []SettingSource             `json:"setting_sources,omitempty"`
	Sandbox                  *SandboxSettings            `json:"sandbox,omitempty"`
	Plugins                  []SdkPluginConfig           `json:"plugins,omitempty"`
	// MaxThinkingTokens is deprecated. Use Thinking instead.
	MaxThinkingTokens *int `json:"max_thinking_tokens,omitempty"`
	// Thinking controls extended thinking behavior. Takes precedence over MaxThinkingTokens.
	Thinking ThinkingConfig `json:"-"`
	// Effort controls thinking depth. Valid values: "low", "medium", "high", "max".
	Effort       *string        `json:"-"`
	OutputFormat map[string]any `json:"output_format,omitempty"`
	// EnableFileCheckpointing enables file checkpointing to track file changes.
	// When enabled, files can be rewound to their state at any user message.
	EnableFileCheckpointing bool `json:"-"`
	// Worktree creates a new git worktree for this session.
	// If non-nil, --worktree is passed. Empty string means auto-named worktree.
	Worktree *string `json:"-"`
}

// SDK Control Protocol types

// SDKControlRequest represents a control request.
type SDKControlRequest struct {
	Type      string         `json:"type"` // "control_request"
	RequestID string         `json:"request_id"`
	Request   map[string]any `json:"request"`
}

// SDKControlResponse represents a control response.
type SDKControlResponse struct {
	Type     string         `json:"type"` // "control_response"
	Response map[string]any `json:"response"`
}

// RawMessage represents an unparsed message from the CLI.
type RawMessage = map[string]any

// ToolAnnotations represents annotations for an MCP tool.
type ToolAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
}

// SdkMcpToolHandler is the handler function type for SDK MCP tools.
type SdkMcpToolHandler func(arguments map[string]any) ([]map[string]any, error)

// SdkMcpTool represents an SDK MCP tool definition.
type SdkMcpTool struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	InputSchema map[string]any    `json:"inputSchema,omitempty"`
	Handler     SdkMcpToolHandler `json:"-"`
	Annotations *ToolAnnotations  `json:"annotations,omitempty"`
}

// SdkMcpServer represents an in-process SDK MCP server.
type SdkMcpServer struct {
	Name    string
	Version string
	Tools   []SdkMcpTool
	mu      sync.RWMutex
}

// NewSdkMcpServer creates a new SDK MCP server.
func NewSdkMcpServer(name, version string) *SdkMcpServer {
	return &SdkMcpServer{
		Name:    name,
		Version: version,
	}
}

// AddTool adds a tool to the SDK MCP server.
func (s *SdkMcpServer) AddTool(tool SdkMcpTool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Tools = append(s.Tools, tool)
}

// GetTools returns all tools from the SDK MCP server.
func (s *SdkMcpServer) GetTools() []SdkMcpTool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tools := make([]SdkMcpTool, len(s.Tools))
	copy(tools, s.Tools)
	return tools
}

// FindTool finds a tool by name.
func (s *SdkMcpServer) FindTool(name string) *SdkMcpTool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.Tools {
		if s.Tools[i].Name == name {
			return &s.Tools[i]
		}
	}
	return nil
}

// MarshalJSON provides custom JSON marshaling for ContentBlock slice.
func marshalContentBlocks(blocks []ContentBlock) ([]byte, error) {
	result := make([]map[string]any, len(blocks))
	for i, block := range blocks {
		switch b := block.(type) {
		case TextBlock:
			result[i] = map[string]any{"type": "text", "text": b.Text}
		case ThinkingBlock:
			result[i] = map[string]any{"type": "thinking", "thinking": b.Thinking, "signature": b.Signature}
		case ToolUseBlock:
			result[i] = map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": b.Input}
		case ToolResultBlock:
			m := map[string]any{"type": "tool_result", "tool_use_id": b.ToolUseID}
			if b.Content != nil {
				m["content"] = b.Content
			}
			if b.IsError != nil {
				m["is_error"] = *b.IsError
			}
			result[i] = m
		}
	}
	return json.Marshal(result)
}
