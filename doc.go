// Package claude provides a Go SDK for interacting with Claude Code.
//
// This SDK allows you to build AI-powered applications using Claude's agentic capabilities.
// It supports both one-shot queries and bidirectional streaming conversations.
//
// # Quick Start
//
// For simple one-shot queries:
//
//	ctx := context.Background()
//	msgChan, errChan := claude.RunQuery(ctx, "What is 2 + 2?", nil)
//
//	for msg := range msgChan {
//	    switch m := msg.(type) {
//	    case *claude.AssistantMessage:
//	        for _, block := range m.Content {
//	            if tb, ok := block.(claude.TextBlock); ok {
//	                fmt.Println("Claude:", tb.Text)
//	            }
//	        }
//	    case *claude.ResultMessage:
//	        if m.TotalCostUSD != nil {
//	            fmt.Printf("Cost: $%.4f\n", *m.TotalCostUSD)
//	        }
//	    }
//	}
//
// # Streaming Mode
//
// For interactive conversations with full control:
//
//	client := claude.NewClaudeSDKClient(&claude.ClaudeAgentOptions{
//	    AllowedTools: []string{"Read", "Write"},
//	    SystemPrompt: "You are a helpful coding assistant.",
//	})
//
//	if err := client.Connect(ctx); err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Close()
//
//	// Send a query
//	if err := client.SendQuery(ctx, "Create a hello.txt file"); err != nil {
//	    log.Fatal(err)
//	}
//
//	// Receive messages
//	msgChan, errChan := client.ReceiveMessages(ctx)
//	for msg := range msgChan {
//	    // Process messages...
//	}
//
// # Features
//
//   - One-shot queries using the RunQuery() function
//   - Bidirectional streaming with ClaudeSDKClient
//   - Tool permission callbacks for custom approval logic
//   - Hook callbacks for PreToolUse, PostToolUse, etc.
//   - Interrupt and dynamic control during conversations
//   - MCP server integration
//   - Sandbox configuration for security
//
// # Architecture
//
// The SDK is built on these core components:
//
//   - Transport: Low-level I/O abstraction (subprocess, etc.)
//   - Query: Control protocol handling
//   - ClaudeSDKClient: High-level client API
//   - Message types: UserMessage, AssistantMessage, SystemMessage, ResultMessage
//   - Content blocks: TextBlock, ThinkingBlock, ToolUseBlock, ToolResultBlock
package claude
