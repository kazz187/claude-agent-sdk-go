// Package main demonstrates streaming mode with ClaudeSDKClient.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	claude "github.com/kazz187/claude-agent-sdk-go"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Basic Streaming Example ===")
	basicStreamingExample(ctx)

	fmt.Println("\n=== Multi-Turn Conversation Example ===")
	multiTurnExample(ctx)

	fmt.Println("\n=== Interrupt Example ===")
	interruptExample(ctx)
}

func basicStreamingExample(ctx context.Context) {
	// Create client
	client := claude.NewClaudeSDKClient(nil)

	// Connect in streaming mode (empty prompt)
	if err := client.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Send a query
	fmt.Println("User: What is 2+2?")
	if err := client.SendQuery(ctx, "What is 2+2?"); err != nil {
		log.Fatalf("Failed to send query: %v", err)
	}

	// Receive response
	messages, err := client.ReceiveResponse(ctx)
	if err != nil {
		log.Fatalf("Failed to receive response: %v", err)
	}

	for _, msg := range messages {
		displayMessage(msg)
	}
}

func multiTurnExample(ctx context.Context) {
	client := claude.NewClaudeSDKClient(nil)

	if err := client.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// First turn
	fmt.Println("User: What's the capital of France?")
	if err := client.SendQuery(ctx, "What's the capital of France?"); err != nil {
		log.Fatalf("Failed to send query: %v", err)
	}

	messages, err := client.ReceiveResponse(ctx)
	if err != nil {
		log.Fatalf("Failed to receive response: %v", err)
	}
	for _, msg := range messages {
		displayMessage(msg)
	}

	// Second turn - follow-up
	fmt.Println("\nUser: What's the population of that city?")
	if err := client.SendQuery(ctx, "What's the population of that city?"); err != nil {
		log.Fatalf("Failed to send query: %v", err)
	}

	messages, err = client.ReceiveResponse(ctx)
	if err != nil {
		log.Fatalf("Failed to receive response: %v", err)
	}
	for _, msg := range messages {
		displayMessage(msg)
	}
}

func interruptExample(ctx context.Context) {
	client := claude.NewClaudeSDKClient(nil)

	if err := client.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// Start a long-running task
	fmt.Println("User: Count from 1 to 100 slowly")
	if err := client.SendQuery(ctx, "Count from 1 to 100 slowly, with a brief pause between each number"); err != nil {
		log.Fatalf("Failed to send query: %v", err)
	}

	// Start receiving in a goroutine
	msgChan, errChan := client.ReceiveMessages(ctx)

	// Set a timer to interrupt after 2 seconds
	go func() {
		time.Sleep(2 * time.Second)
		fmt.Println("\n[After 2 seconds, sending interrupt...]")
		if err := client.Interrupt(); err != nil {
			log.Printf("Interrupt failed: %v", err)
		}
	}()

	// Receive messages until interrupted
	for {
		select {
		case err := <-errChan:
			if err != nil {
				log.Printf("Error: %v", err)
			}
			return
		case msg, ok := <-msgChan:
			if !ok {
				// After interrupt, send a new query
				fmt.Println("\nUser: Never mind, just tell me a quick joke")
				if err := client.SendQuery(ctx, "Never mind, just tell me a quick joke"); err != nil {
					log.Fatalf("Failed to send query: %v", err)
				}

				// Get the joke
				messages, err := client.ReceiveResponse(ctx)
				if err != nil {
					log.Fatalf("Failed to receive response: %v", err)
				}
				for _, m := range messages {
					displayMessage(m)
				}
				return
			}
			displayMessage(msg)

			// Check if this is a result message (end of response)
			if _, isResult := msg.(*claude.ResultMessage); isResult {
				return
			}
		}
	}
}

func displayMessage(msg claude.Message) {
	switch m := msg.(type) {
	case *claude.UserMessage:
		if s := m.GetContentString(); s != "" {
			fmt.Printf("User: %s\n", s)
		}
		for _, block := range m.GetContentBlocks() {
			if tb, ok := block.(claude.TextBlock); ok {
				fmt.Printf("User: %s\n", tb.Text)
			}
		}
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
		fmt.Println("Response complete.")
		if m.TotalCostUSD != nil {
			fmt.Printf("Cost: $%.4f\n", *m.TotalCostUSD)
		}
	}
}
