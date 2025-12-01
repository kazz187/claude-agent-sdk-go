package claude

import (
	"context"
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
//
// Example:
//
//	msgChan, errChan := claude.RunQuery(ctx, "What is 2 + 2?", nil)
//	for msg := range msgChan {
//	    switch m := msg.(type) {
//	    case *claude.AssistantMessage:
//	        for _, block := range m.Content {
//	            if tb, ok := block.(claude.TextBlock); ok {
//	                fmt.Println(tb.Text)
//	            }
//	        }
//	    case *claude.ResultMessage:
//	        fmt.Printf("Cost: $%.4f\n", *m.TotalCostUSD)
//	    }
//	}
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

		// Create subprocess transport with prompt (non-streaming mode)
		transport := NewSubprocessTransport(prompt, opts)

		if err := transport.Connect(ctx); err != nil {
			errChan <- err
			return
		}
		defer transport.Close()

		// Read messages
		rawMsgChan, rawErrChan := transport.ReadMessages(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-rawErrChan:
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
