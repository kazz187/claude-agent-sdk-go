package claudeagent

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"
)

// QueryResult contains the result of a one-shot query.
type QueryResult struct {
	Messages []Message
	Result   *ResultMessage
}

// RunQuery executes a one-shot query to Claude and returns a channel of messages.
// This is a convenience function for simple queries that don't require
// bidirectional communication.
//
// For more control over the conversation, use ClaudeSDKClient instead.
func RunQuery(ctx context.Context, prompt string, options *ClaudeAgentOptions) (<-chan Message, <-chan error) {
	msgChan := make(chan Message, 100)
	errChan := make(chan error, 1)

	go func() {
		defer close(msgChan)
		defer close(errChan)

		opts := ClaudeAgentOptions{}
		if options != nil {
			opts = *options
		}

		// Validate and configure permission settings
		if opts.CanUseTool != nil {
			if opts.PermissionPromptToolName != "" {
				errChan <- NewCLIConnectionError("CanUseTool callback cannot be used with PermissionPromptToolName", nil)
				return
			}
			opts.PermissionPromptToolName = "stdio"
		}

		// Create subprocess transport (always streaming mode)
		transport := NewSubprocessTransport("", opts)

		if err := transport.Connect(ctx); err != nil {
			errChan <- err
			return
		}
		defer transport.Close()

		// Extract SDK MCP servers
		sdkMcpServers := make(map[string]*SdkMcpServer)

		// Calculate initialize timeout.
		// This is separate from CLAUDE_CODE_STREAM_CLOSE_TIMEOUT which controls
		// how long stdin stays open for the control protocol. The initialize
		// timeout only covers the CLI startup handshake and should remain short.
		initTimeout := 60 * time.Second
		if timeoutStr := os.Getenv("CLAUDE_CODE_INIT_TIMEOUT"); timeoutStr != "" {
			if ms, err := strconv.ParseInt(timeoutStr, 10, 64); err == nil {
				initTimeout = time.Duration(ms) * time.Millisecond
			}
		}

		// Create Query with full options
		query := NewQuery(transport, true, QueryOptions{
			CanUseTool:        opts.CanUseTool,
			Hooks:             opts.Hooks,
			SdkMcpServers:     sdkMcpServers,
			Agents:            opts.Agents,
			InitializeTimeout: initTimeout,
		})

		// Start reading messages
		if err := query.Start(ctx); err != nil {
			errChan <- err
			return
		}
		defer query.Close()

		// Initialize (sends hooks, agents via control protocol)
		if _, err := query.Initialize(); err != nil {
			errChan <- err
			return
		}

		// Send the user message via stdin
		message := userInputMessage{
			Type: MessageTypeUser,
			Message: userInputContent{
				Role:    MessageRoleUser,
				Content: prompt,
			},
			SessionID: "default",
		}

		data, err := json.Marshal(message)
		if err != nil {
			errChan <- err
			return
		}

		if err := transport.Write(ctx, string(data)+"\n"); err != nil {
			errChan <- err
			return
		}

		// If we have SDK MCP servers, hooks, or CanUseTool callback, wait for first result
		// before ending input. CanUseTool needs stdin open for control protocol responses.
		hasHooks := len(opts.Hooks) > 0
		hasSdkMcp := len(sdkMcpServers) > 0
		hasCanUseTool := opts.CanUseTool != nil
		if hasSdkMcp || hasHooks || hasCanUseTool {
			// Let StreamInput handle the wait
			go func() {
				streamCloseTimeout := 60 * time.Second
				if timeoutStr := os.Getenv("CLAUDE_CODE_STREAM_CLOSE_TIMEOUT"); timeoutStr != "" {
					if ms, err := strconv.ParseInt(timeoutStr, 10, 64); err == nil {
						streamCloseTimeout = time.Duration(ms) * time.Millisecond
					}
				}
				timer := time.NewTimer(streamCloseTimeout)
				defer timer.Stop()
				select {
				case <-query.firstResultChan:
				case <-timer.C:
				case <-ctx.Done():
				}
				transport.EndInput()
			}()
		} else {
			transport.EndInput()
		}

		// Read and parse messages
		rawMsgChan := query.ReceiveMessages()
		queryErrChan := query.Errors()

		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-queryErrChan:
				if ok && err != nil {
					// Check if it's a process error with exit code 0 (normal termination)
					if pe, ok := err.(*ProcessError); ok && pe.ExitCode == 0 {
						return
					}
					errChan <- err
					return
				}
			case rawMsg, ok := <-rawMsgChan:
				if !ok {
					return
				}

				msg, err := ParseMessage(rawMsg)
				if err != nil {
					errChan <- err
					continue
				}

				// Skip nil messages (unknown types)
				if msg == nil {
					continue
				}

				select {
				case msgChan <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return msgChan, errChan
}

// RunQuerySync executes a synchronous query and returns all messages.
// This blocks until the query completes or the context is cancelled.
func RunQuerySync(ctx context.Context, prompt string, options *ClaudeAgentOptions) (*QueryResult, error) {
	msgChan, errChan := RunQuery(ctx, prompt, options)

	result := &QueryResult{
		Messages: make([]Message, 0),
	}

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case err, ok := <-errChan:
			if ok && err != nil {
				// MessageParseError is non-fatal: skip unparseable messages
				// (e.g. user messages echoed back in a newer CLI format).
				if _, isParseErr := err.(*MessageParseError); isParseErr {
					continue
				}
				return result, err
			}
		case msg, ok := <-msgChan:
			if !ok {
				return result, nil
			}
			result.Messages = append(result.Messages, msg)

			if rm, ok := msg.(*ResultMessage); ok {
				result.Result = rm
				return result, nil
			}
		}
	}
}
