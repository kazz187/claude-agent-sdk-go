// Package main demonstrates basic usage of the Claude SDK.
package main

import (
	"context"
	"fmt"
	"log"

	claude "github.com/kazz187/claude-agent-sdk-go"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Basic Example ===")
	basicExample(ctx)

	fmt.Println("\n=== With Options Example ===")
	withOptionsExample(ctx)

	fmt.Println("\n=== With Tools Example ===")
	withToolsExample(ctx)
}

func basicExample(ctx context.Context) {
	// Simple question using RunQuery function
	msgChan, errChan := claude.RunQuery(ctx, "What is 2 + 2?", nil)

	for {
		select {
		case err := <-errChan:
			if err != nil {
				log.Printf("Error: %v", err)
			}
		case msg, ok := <-msgChan:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *claude.AssistantMessage:
				for _, block := range m.Content {
					if tb, ok := block.(claude.TextBlock); ok {
						fmt.Printf("Claude: %s\n", tb.Text)
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

func withOptionsExample(ctx context.Context) {
	// Query with custom options
	maxTurns := 1
	options := &claude.ClaudeAgentOptions{
		SystemPrompt: "You are a helpful assistant that explains things simply.",
		MaxTurns:     &maxTurns,
	}

	msgChan, errChan := claude.RunQuery(ctx, "Explain what Go is in one sentence.", options)

	for {
		select {
		case err := <-errChan:
			if err != nil {
				log.Printf("Error: %v", err)
			}
		case msg, ok := <-msgChan:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *claude.AssistantMessage:
				for _, block := range m.Content {
					if tb, ok := block.(claude.TextBlock); ok {
						fmt.Printf("Claude: %s\n", tb.Text)
					}
				}
			}
		}
	}
}

func withToolsExample(ctx context.Context) {
	// Query with tools enabled
	options := &claude.ClaudeAgentOptions{
		AllowedTools: []string{"Read", "Write"},
		SystemPrompt: "You are a helpful file assistant.",
	}

	msgChan, errChan := claude.RunQuery(ctx, "Create a file called hello.txt with 'Hello, World!' in it", options)

	for {
		select {
		case err := <-errChan:
			if err != nil {
				log.Printf("Error: %v", err)
			}
		case msg, ok := <-msgChan:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *claude.AssistantMessage:
				for _, block := range m.Content {
					if tb, ok := block.(claude.TextBlock); ok {
						fmt.Printf("Claude: %s\n", tb.Text)
					}
					if tu, ok := block.(claude.ToolUseBlock); ok {
						fmt.Printf("Tool Use: %s\n", tu.Name)
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
