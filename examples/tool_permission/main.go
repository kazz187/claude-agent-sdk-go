// Package main demonstrates custom tool permission callbacks.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	claude "github.com/kazz187/claude-agent-sdk-go"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Tool Permission Callback Example ===")
	toolPermissionExample(ctx)
}

func toolPermissionExample(ctx context.Context) {
	// Define a custom tool permission callback
	canUseTool := func(toolName string, input map[string]any, permCtx claude.ToolPermissionContext) (claude.PermissionResult, error) {
		fmt.Printf("\n[Permission Request]\n")
		fmt.Printf("  Tool: %s\n", toolName)
		fmt.Printf("  Input: %v\n", input)

		// Example: Allow Read, but require confirmation for Write
		switch toolName {
		case "Read":
			fmt.Println("  Decision: Allow (Read is always allowed)")
			return claude.PermissionResultAllow{
				Behavior: "allow",
			}, nil

		case "Write":
			// Check if writing to a safe location
			if path, ok := input["file_path"].(string); ok {
				if strings.HasPrefix(path, "/tmp/") {
					fmt.Println("  Decision: Allow (writing to /tmp)")
					return claude.PermissionResultAllow{
						Behavior: "allow",
					}, nil
				}
			}
			fmt.Println("  Decision: Deny (writing outside /tmp not allowed)")
			return claude.PermissionResultDeny{
				Behavior: "deny",
				Message:  "Writing files outside /tmp is not allowed",
			}, nil

		case "Bash":
			// Check the command
			if cmd, ok := input["command"].(string); ok {
				// Block dangerous commands
				dangerous := []string{"rm -rf", "sudo", "> /dev/"}
				for _, d := range dangerous {
					if strings.Contains(cmd, d) {
						fmt.Printf("  Decision: Deny (dangerous command: %s)\n", d)
						return claude.PermissionResultDeny{
							Behavior: "deny",
							Message:  fmt.Sprintf("Command contains dangerous pattern: %s", d),
						}, nil
					}
				}
				fmt.Println("  Decision: Allow (command appears safe)")
				return claude.PermissionResultAllow{
					Behavior: "allow",
				}, nil
			}

		default:
			fmt.Printf("  Decision: Allow (tool %s is allowed by default)\n", toolName)
			return claude.PermissionResultAllow{
				Behavior: "allow",
			}, nil
		}

		return claude.PermissionResultAllow{
			Behavior: "allow",
		}, nil
	}

	// Create client with custom permission callback
	options := &claude.ClaudeAgentOptions{
		AllowedTools: []string{"Read", "Write", "Bash"},
		CanUseTool:   canUseTool,
	}

	client := claude.NewClaudeSDKClient(options)

	// Connect in streaming mode (required for permission callbacks)
	if err := client.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Try various operations
	prompts := []string{
		"Read the contents of /etc/hostname",
		"Create a file at /tmp/test.txt with 'Hello'",
		"Run the command 'echo Hello from bash'",
	}

	for _, prompt := range prompts {
		fmt.Printf("\n--- Query: %s ---\n", prompt)

		if err := client.SendQuery(ctx, prompt); err != nil {
			log.Fatalf("Failed to send query: %v", err)
		}

		messages, err := client.ReceiveResponse(ctx)
		if err != nil {
			log.Fatalf("Failed to receive response: %v", err)
		}

		for _, msg := range messages {
			switch m := msg.(type) {
			case *claude.AssistantMessage:
				for _, block := range m.Content {
					if tb, ok := block.(claude.TextBlock); ok {
						fmt.Printf("Claude: %s\n", tb.Text)
					}
				}
			case *claude.ResultMessage:
				fmt.Println("Query completed.")
			}
		}
	}
}
