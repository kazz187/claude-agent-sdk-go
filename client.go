package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// ClaudeSDKClient provides a client for bidirectional, interactive conversations with Claude Code.
//
// This client provides full control over the conversation flow with support
// for streaming, interrupts, and dynamic message sending. For simple one-shot
// queries, consider using the Query() function instead.
//
// Key features:
//   - Bidirectional: Send and receive messages at any time
//   - Stateful: Maintains conversation context across messages
//   - Interactive: Send follow-ups based on responses
//   - Control flow: Support for interrupts and session management
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
		// Streaming mode: empty prompt
		c.transport = NewSubprocessTransport(prompt, opts)
	}

	if err := c.transport.Connect(ctx); err != nil {
		c.transport = nil
		return err
	}

	// Calculate initialize timeout from env var if set
	initTimeout := 60 * time.Second
	if timeoutStr := os.Getenv("CLAUDE_CODE_STREAM_CLOSE_TIMEOUT"); timeoutStr != "" {
		if ms, err := time.ParseDuration(timeoutStr + "ms"); err == nil {
			if ms > initTimeout {
				initTimeout = ms
			}
		}
	}

	// Create Query to handle control protocol
	isStreaming := prompt == ""
	c.query = NewQuery(c.transport, isStreaming, QueryOptions{
		CanUseTool:        opts.CanUseTool,
		Hooks:             c.convertHooksToInternalFormat(opts.Hooks),
		InitializeTimeout: initTimeout,
	})

	// Start reading messages and initialize
	if err := c.query.Start(ctx); err != nil {
		c.transport.Close()
		c.transport = nil
		return err
	}

	if isStreaming {
		if _, err := c.query.Initialize(); err != nil {
			c.query.Close()
			c.query = nil
			c.transport = nil
			return err
		}
	}

	// Start the message parsing goroutine
	c.parsedMsgChan = make(chan Message, 100)
	c.parsedErrChan = make(chan error, 1)
	go c.parseMessages(ctx)

	return nil
}

// parseMessages is a single goroutine that parses raw messages into typed messages.
// This runs for the lifetime of the connection.
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

			select {
			case c.parsedMsgChan <- msg:
			case <-ctx.Done():
				return
			}
		}
	}
}

// ReceiveMessages returns channels for receiving parsed messages and errors from Claude.
// The message channel yields parsed Message objects until the connection is closed.
// The error channel reports any errors that occur during message reception.
//
// IMPORTANT: These are shared channels - all callers receive from the same channels.
// For most use cases, use ReceiveResponse which reads until a ResultMessage is received.
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
//
// Valid modes:
//   - "default": CLI prompts for dangerous tools
//   - "acceptEdits": Auto-accept file edits
//   - "bypassPermissions": Allow all tools (use with caution)
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
func (c *ClaudeSDKClient) SetModel(model string) error {
	c.mu.Lock()
	query := c.query
	c.mu.Unlock()

	if query == nil {
		return ErrNotConnected
	}

	return query.SetModel(model)
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

// ReceiveResponse is a convenience method that receives messages until a ResultMessage is received.
// It returns all received messages including the final ResultMessage.
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
