# Claude Agent SDK for Go

A Go SDK for interacting with Claude Code, providing programmatic access to Claude's agentic capabilities.

This SDK is a Go port of the official [claude-agent-sdk-python](https://github.com/anthropics/claude-agent-sdk-python).

## Features

- **One-shot queries**: Simple function for single queries
- **Streaming mode**: Bidirectional, interactive conversations
- **Tool permission callbacks**: Custom approval logic for tool usage
- **Hook callbacks**: Intercept PreToolUse, PostToolUse, and other events
- **Dynamic control**: Interrupt, change models, and modify permissions during conversations
- **MCP server integration**: Connect to Model Context Protocol servers
- **Sandbox support**: Configure bash command isolation

## Requirements

- Go 1.21 or later
- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed

Install Claude Code CLI:
```bash
npm install -g @anthropic-ai/claude-code
```

## Installation

```bash
go get github.com/kazz187/claude-agent-sdk-go
```

## Quick Start

### One-shot Query

```go
package main

import (
    "context"
    "fmt"
    "log"

    claude "github.com/kazz187/claude-agent-sdk-go"
)

func main() {
    ctx := context.Background()

    msgChan, errChan := claude.RunQuery(ctx, "What is 2 + 2?", nil)

    for {
        select {
        case err := <-errChan:
            if err != nil {
                log.Fatal(err)
            }
        case msg, ok := <-msgChan:
            if !ok {
                return
            }
            switch m := msg.(type) {
            case *claude.AssistantMessage:
                for _, block := range m.Content {
                    if tb, ok := block.(claude.TextBlock); ok {
                        fmt.Println("Claude:", tb.Text)
                    }
                }
            case *claude.ResultMessage:
                if m.TotalCostUSD != nil {
                    fmt.Printf("Cost: $%.4f\n", *m.TotalCostUSD)
                }
            }
        }
    }
}
```

### Streaming Mode (Interactive)

```go
package main

import (
    "context"
    "fmt"
    "log"

    claude "github.com/kazz187/claude-agent-sdk-go"
)

func main() {
    ctx := context.Background()

    client := claude.NewClaudeSDKClient(&claude.ClaudeAgentOptions{
        AllowedTools: []string{"Read", "Write"},
        SystemPrompt: "You are a helpful coding assistant.",
    })

    if err := client.Connect(ctx); err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    // Send a query
    if err := client.SendQuery(ctx, "Create a hello.txt file"); err != nil {
        log.Fatal(err)
    }

    // Receive response
    messages, err := client.ReceiveResponse(ctx)
    if err != nil {
        log.Fatal(err)
    }

    for _, msg := range messages {
        switch m := msg.(type) {
        case *claude.AssistantMessage:
            for _, block := range m.Content {
                if tb, ok := block.(claude.TextBlock); ok {
                    fmt.Println("Claude:", tb.Text)
                }
            }
        }
    }
}
```

### Tool Permission Callback

```go
canUseTool := func(toolName string, input map[string]any, ctx claude.ToolPermissionContext) (claude.PermissionResult, error) {
    fmt.Printf("Tool requested: %s\n", toolName)

    switch toolName {
    case "Read":
        return claude.PermissionResultAllow{Behavior: "allow"}, nil
    case "Bash":
        if cmd, ok := input["command"].(string); ok {
            if strings.Contains(cmd, "rm -rf") {
                return claude.PermissionResultDeny{
                    Behavior: "deny",
                    Message:  "Dangerous command not allowed",
                }, nil
            }
        }
        return claude.PermissionResultAllow{Behavior: "allow"}, nil
    default:
        return claude.PermissionResultAllow{Behavior: "allow"}, nil
    }
}

client := claude.NewClaudeSDKClient(&claude.ClaudeAgentOptions{
    AllowedTools: []string{"Read", "Write", "Bash"},
    CanUseTool:   canUseTool,
})
```

## API Reference

### Functions

- `RunQuery(ctx, prompt, options)` - Execute a one-shot query
- `RunQuerySync(ctx, prompt, options)` - Execute a synchronous query (blocking)

### Types

- `ClaudeSDKClient` - Main client for streaming mode
- `ClaudeAgentOptions` - Configuration options
- `Message` - Interface for all message types
- `AssistantMessage` - Claude's response with content blocks
- `UserMessage` - User messages
- `SystemMessage` - System messages
- `ResultMessage` - Query result with cost and usage info
- `TextBlock`, `ToolUseBlock`, `ToolResultBlock`, `ThinkingBlock` - Content blocks

### ClaudeSDKClient Methods

- `Connect(ctx)` - Connect in streaming mode
- `SendQuery(ctx, prompt)` - Send a query
- `ReceiveMessages(ctx)` - Receive messages as channels
- `ReceiveResponse(ctx)` - Receive all messages until ResultMessage
- `Interrupt()` - Send interrupt signal
- `SetPermissionMode(mode)` - Change permission mode
- `SetModel(model)` - Change AI model
- `GetServerInfo()` - Get server initialization info
- `Close()` - Disconnect

## Examples

See the `examples/` directory for more examples:

- `quick_start/` - Basic usage examples
- `streaming_mode/` - Interactive streaming examples
- `tool_permission/` - Custom tool permission callbacks

## Architecture

The SDK follows the same architecture as the Python SDK:

```
┌─────────────────────────────────────────┐
│            ClaudeSDKClient              │
│  (High-level API for users)             │
└────────────────────┬────────────────────┘
                     │
┌────────────────────▼────────────────────┐
│                 Query                    │
│  (Control protocol, hooks, permissions) │
└────────────────────┬────────────────────┘
                     │
┌────────────────────▼────────────────────┐
│              Transport                   │
│  (Abstract I/O interface)               │
└────────────────────┬────────────────────┘
                     │
┌────────────────────▼────────────────────┐
│         SubprocessTransport             │
│  (Claude CLI subprocess communication)  │
└─────────────────────────────────────────┘
```

## License

MIT License

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.
