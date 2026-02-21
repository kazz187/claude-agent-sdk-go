package claudeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// ClaudeSDKClient provides a client for bidirectional, interactive conversations with Claude Code.
type ClaudeSDKClient struct {
	options         ClaudeAgentOptions
	customTransport Transport
	transport       Transport
	query           *Query

	// Parsed message channels (created once per connection)
	parsedMsgChan chan Message
	parsedErrChan chan error

	mu     sync.Mutex
	closed bool
}

// NewClaudeSDKClient creates a new ClaudeSDKClient.
func NewClaudeSDKClient(options *ClaudeAgentOptions) *ClaudeSDKClient {
	opts := ClaudeAgentOptions{}
	if options != nil {
		opts = *options
	}

	// Set SDK entrypoint
	os.Setenv("CLAUDE_CODE_ENTRYPOINT", "sdk-go-client")

	return &ClaudeSDKClient{
		options: opts,
	}
}

// NewClaudeSDKClientWithTransport creates a new ClaudeSDKClient with a custom transport.
func NewClaudeSDKClientWithTransport(options *ClaudeAgentOptions, transport Transport) *ClaudeSDKClient {
	client := NewClaudeSDKClient(options)
	client.customTransport = transport
	return client
}

// convertHooksToInternalFormat converts HookMatcher format to internal Query format.
func (c *ClaudeSDKClient) convertHooksToInternalFormat(hooks map[HookEvent][]HookMatcher) map[HookEvent][]HookMatcher {
	// Hooks are already in the correct format
	return hooks
}

// Connect connects to Claude with an optional initial prompt.
// If prompt is empty, the client enters streaming mode for interactive use.
func (c *ClaudeSDKClient) Connect(ctx context.Context) error {
	return c.ConnectWithPrompt(ctx, "")
}

// ConnectWithPrompt connects to Claude with a prompt.
// Pass empty string for streaming mode without an initial prompt.
func (c *ClaudeSDKClient) ConnectWithPrompt(ctx context.Context, prompt string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport != nil {
		return ErrAlreadyConnected
	}

	// Validate and configure permission settings
	opts := c.options
	if opts.CanUseTool != nil {
		// canUseTool callback requires streaming mode
		if prompt != "" {
			return fmt.Errorf("CanUseTool callback requires streaming mode; pass empty prompt")
		}

		// canUseTool and permission_prompt_tool_name are mutually exclusive
		if opts.PermissionPromptToolName != "" {
			return fmt.Errorf("CanUseTool callback cannot be used with PermissionPromptToolName")
		}

		// Automatically set permission_prompt_tool_name to "stdio" for control protocol
		opts.PermissionPromptToolName = "stdio"
	}

	// Use provided custom transport or create subprocess transport
	if c.customTransport != nil {
		c.transport = c.customTransport
	} else {
		// Always streaming mode
		c.transport = NewSubprocessTransport(prompt, opts)
	}

	if err := c.transport.Connect(ctx); err != nil {
		c.transport = nil
		return err
	}

	// Extract SDK MCP servers from options
	sdkMcpServers := make(map[string]*SdkMcpServer)
	if opts.McpServers != nil {
		for name, config := range opts.McpServers {
			if config.Type == McpServerTypeSDK {
				// SDK MCP servers need to have an associated SdkMcpServer instance
				// stored externally. For now, we skip ones without instances.
				// Users should use the SdkMcpServers field on ClaudeAgentOptions
				// or provide them through the McpServerConfig mechanism.
				_ = name
			}
		}
	}

	// Convert agents to dict format for initialize request
	var agentsDict map[string]map[string]any
	if opts.Agents != nil {
		agentsDict = make(map[string]map[string]any)
		for name, agent := range opts.Agents {
			agentMap := map[string]any{
				"description": agent.Description,
				"prompt":      agent.Prompt,
			}
			if len(agent.Tools) > 0 {
				agentMap["tools"] = agent.Tools
			}
			if agent.Model != "" {
				agentMap["model"] = agent.Model
			}
			agentsDict[name] = agentMap
		}
	}

	// Calculate initialize timeout from env var if set
	initTimeout := 60 * time.Second
	if timeoutStr := os.Getenv("CLAUDE_CODE_STREAM_CLOSE_TIMEOUT"); timeoutStr != "" {
		if ms, err := strconv.ParseInt(timeoutStr, 10, 64); err == nil {
			t := time.Duration(ms) * time.Millisecond
			if t > initTimeout {
				initTimeout = t
			}
		}
	}

	// Create Query to handle control protocol
	isStreaming := true // ClaudeSDKClient always uses streaming mode
	c.query = NewQuery(c.transport, isStreaming, QueryOptions{
		CanUseTool:        opts.CanUseTool,
		Hooks:             c.convertHooksToInternalFormat(opts.Hooks),
		SdkMcpServers:     sdkMcpServers,
		Agents:            agentsDict,
		InitializeTimeout: initTimeout,
	})

	// Start reading messages and initialize
	if err := c.query.Start(ctx); err != nil {
		c.transport.Close()
		c.transport = nil
		return err
	}

	if _, err := c.query.Initialize(); err != nil {
		c.query.Close()
		c.query = nil
		c.transport = nil
		return err
	}

	// Start the message parsing goroutine
	c.parsedMsgChan = make(chan Message, 100)
	c.parsedErrChan = make(chan error, 1)
	go c.parseMessages(ctx)

	return nil
}

// parseMessages is a single goroutine that parses raw messages into typed messages.
func (c *ClaudeSDKClient) parseMessages(ctx context.Context) {
	defer close(c.parsedMsgChan)
	defer close(c.parsedErrChan)

	rawMsgChan := c.query.ReceiveMessages()
	queryErrChan := c.query.Errors()

	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-queryErrChan:
			if ok && err != nil {
				select {
				case c.parsedErrChan <- err:
				default:
				}
				return
			}
		case rawMsg, ok := <-rawMsgChan:
			if !ok {
				return
			}

			msg, err := ParseMessage(rawMsg)
			if err != nil {
				select {
				case c.parsedErrChan <- err:
				default:
				}
				continue
			}

			// Skip nil messages (unknown types - forward compatible)
			if msg == nil {
				continue
			}

			select {
			case c.parsedMsgChan <- msg:
			case <-ctx.Done():
				return
			}
		}
	}
}

// ReceiveMessages returns channels for receiving parsed messages and errors from Claude.
func (c *ClaudeSDKClient) ReceiveMessages(ctx context.Context) (<-chan Message, <-chan error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.parsedMsgChan == nil {
		msgChan := make(chan Message)
		errChan := make(chan error, 1)
		close(msgChan)
		errChan <- ErrNotConnected
		close(errChan)
		return msgChan, errChan
	}

	return c.parsedMsgChan, c.parsedErrChan
}

// SendQuery sends a new message/query in streaming mode.
func (c *ClaudeSDKClient) SendQuery(ctx context.Context, prompt string) error {
	return c.SendQueryWithSessionID(ctx, prompt, "default")
}

// SendQueryWithSessionID sends a new message/query with a specific session ID.
func (c *ClaudeSDKClient) SendQueryWithSessionID(ctx context.Context, prompt string, sessionID string) error {
	c.mu.Lock()
	transport := c.transport
	c.mu.Unlock()

	if transport == nil {
		return ErrNotConnected
	}

	message := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": prompt,
		},
		"parent_tool_use_id": nil,
		"session_id":         sessionID,
	}

	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	return transport.Write(ctx, string(data)+"\n")
}

// SendQueryStream sends messages from a channel in streaming mode.
func (c *ClaudeSDKClient) SendQueryStream(ctx context.Context, messages <-chan map[string]any) error {
	c.mu.Lock()
	query := c.query
	c.mu.Unlock()

	if query == nil {
		return ErrNotConnected
	}

	return query.StreamInput(ctx, messages)
}

// Interrupt sends an interrupt signal (only works with streaming mode).
func (c *ClaudeSDKClient) Interrupt() error {
	c.mu.Lock()
	query := c.query
	c.mu.Unlock()

	if query == nil {
		return ErrNotConnected
	}

	return query.Interrupt()
}

// SetPermissionMode changes the permission mode during conversation (streaming mode only).
func (c *ClaudeSDKClient) SetPermissionMode(mode PermissionMode) error {
	c.mu.Lock()
	query := c.query
	c.mu.Unlock()

	if query == nil {
		return ErrNotConnected
	}

	return query.SetPermissionMode(string(mode))
}

// SetModel changes the AI model during conversation (streaming mode only).
// Pass nil to use the default model.
func (c *ClaudeSDKClient) SetModel(model *string) error {
	c.mu.Lock()
	query := c.query
	c.mu.Unlock()

	if query == nil {
		return ErrNotConnected
	}

	return query.SetModel(model)
}

// RewindFiles rewinds tracked files to their state at a specific user message.
// Requires EnableFileCheckpointing to be set in options.
func (c *ClaudeSDKClient) RewindFiles(ctx context.Context, userMessageID string) error {
	c.mu.Lock()
	query := c.query
	c.mu.Unlock()

	if query == nil {
		return ErrNotConnected
	}

	return query.RewindFiles(userMessageID)
}

// GetMcpStatus gets current MCP server connection status.
func (c *ClaudeSDKClient) GetMcpStatus(ctx context.Context) (map[string]any, error) {
	c.mu.Lock()
	query := c.query
	c.mu.Unlock()

	if query == nil {
		return nil, ErrNotConnected
	}

	return query.GetMcpStatus()
}

// GetServerInfo returns server initialization info including available commands.
func (c *ClaudeSDKClient) GetServerInfo() map[string]any {
	c.mu.Lock()
	query := c.query
	c.mu.Unlock()

	if query == nil {
		return nil
	}

	return query.GetInitializationResult()
}

// Close disconnects from Claude and releases resources.
func (c *ClaudeSDKClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	if c.query != nil {
		c.query.Close()
		c.query = nil
	}

	if c.transport != nil && c.customTransport == nil {
		c.transport.Close()
	}
	c.transport = nil

	return nil
}

// Disconnect is an alias for Close.
func (c *ClaudeSDKClient) Disconnect() error {
	return c.Close()
}

// ReceiveResponse is a convenience method that receives messages until a ResultMessage is received.
func (c *ClaudeSDKClient) ReceiveResponse(ctx context.Context) ([]Message, error) {
	c.mu.Lock()
	msgChan := c.parsedMsgChan
	errChan := c.parsedErrChan
	c.mu.Unlock()

	if msgChan == nil {
		return nil, ErrNotConnected
	}

	var messages []Message

	for {
		select {
		case <-ctx.Done():
			return messages, ctx.Err()
		case err := <-errChan:
			if err != nil {
				return messages, err
			}
		case msg, ok := <-msgChan:
			if !ok {
				return messages, nil
			}
			messages = append(messages, msg)

			// Stop after receiving ResultMessage
			if _, isResult := msg.(*ResultMessage); isResult {
				return messages, nil
			}
		}
	}
}
