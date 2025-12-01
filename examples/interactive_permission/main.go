// Package main demonstrates interactive tool permission prompts.
// This example shows how to ask the user for Yes/No confirmation before allowing tool use.
//
// IMPORTANT: The canUseTool callback is only invoked when Claude needs permission
// that isn't already covered by permission rules or hooks. This typically happens
// when writing files to new locations or running potentially dangerous commands.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	claude "github.com/kazz187/claude-agent-sdk-go"
)

func main() {
	ctx := context.Background()

	fmt.Println("=== Interactive Tool Permission Example ===")
	fmt.Println("Claude will ask for your permission before using tools that need it.")
	fmt.Println("(Note: Some safe operations may be auto-approved by the CLI)")
	fmt.Println()

	interactivePermissionExample(ctx)
}

// askUserPermission prompts the user with Yes/No and returns the result.
func askUserPermission(toolName string, input map[string]any) bool {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════════════╗")
	fmt.Println("║              TOOL PERMISSION REQUEST                       ║")
	fmt.Println("╠════════════════════════════════════════════════════════════╣")
	fmt.Printf("║ Tool: %-54s ║\n", toolName)
	fmt.Println("╠════════════════════════════════════════════════════════════╣")

	// Display tool-specific input
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			displayCmd := cmd
			if len(displayCmd) > 50 {
				displayCmd = displayCmd[:47] + "..."
			}
			fmt.Printf("║ Command: %-51s ║\n", displayCmd)
		}
	case "Write", "Read", "Edit":
		if path, ok := input["file_path"].(string); ok {
			displayPath := path
			if len(displayPath) > 50 {
				displayPath = "..." + displayPath[len(displayPath)-47:]
			}
			fmt.Printf("║ File: %-54s ║\n", displayPath)
		}
	}

	fmt.Println("╠════════════════════════════════════════════════════════════╣")
	fmt.Println("║ Allow this tool to execute? (Y/N)                          ║")
	fmt.Println("╚════════════════════════════════════════════════════════════╝")
	fmt.Print("→ ")

	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Error reading input, denying by default")
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

func interactivePermissionExample(ctx context.Context) {
	// Define the interactive tool permission callback
	canUseTool := func(toolName string, input map[string]any, permCtx claude.ToolPermissionContext) (claude.PermissionResult, error) {
		allowed := askUserPermission(toolName, input)

		if allowed {
			fmt.Println("✓ Permission GRANTED")
			return claude.PermissionResultAllow{Behavior: "allow"}, nil
		}

		fmt.Println("✗ Permission DENIED")
		return claude.PermissionResultDeny{
			Behavior: "deny",
			Message:  fmt.Sprintf("User denied permission for tool: %s", toolName),
		}, nil
	}

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}

	// Create client with permission callback
	// PermissionMode must be "default" for canUseTool to be invoked
	// Cwd ensures Claude uses the correct working directory
	options := &claude.ClaudeAgentOptions{
		CanUseTool:     canUseTool,
		PermissionMode: claude.PermissionModeDefault,
		Cwd:            cwd,
	}

	client := claude.NewClaudeSDKClient(options)

	if err := client.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	// This will trigger permission request because it writes to current directory
	// Using /tmp to ensure predictable path behavior
	prompt := "Create a file called /tmp/permission_demo.txt with 'Hello from the Go SDK!'"

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("📝 User: %s\n", prompt)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	if err := client.SendQuery(ctx, prompt); err != nil {
		log.Fatalf("Failed to send query: %v", err)
	}

	messages, err := client.ReceiveResponse(ctx)
	if err != nil {
		log.Fatalf("Failed to receive response: %v", err)
	}

	fmt.Println()
	for _, msg := range messages {
		switch m := msg.(type) {
		case *claude.AssistantMessage:
			for _, block := range m.Content {
				switch b := block.(type) {
				case claude.TextBlock:
					fmt.Printf("🤖 Claude: %s\n", b.Text)
				case claude.ToolUseBlock:
					fmt.Printf("🔧 Tool Use: %s\n", b.Name)
				}
			}
		case *claude.ResultMessage:
			fmt.Println()
			fmt.Println("✅ Query completed.")
		}
	}
}
