package claudeagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const (
	defaultMaxBufferSize     = 1024 * 1024 // 1MB buffer limit
	minimumClaudeCodeVersion = "2.0.0"
	sdkVersion               = "0.1.0"
)

// cmdLengthLimit is platform-specific command line length limit.
var cmdLengthLimit = func() int {
	if runtime.GOOS == "windows" {
		return 8000
	}
	return 100000
}()

// SubprocessTransport implements Transport using Claude Code CLI subprocess.
type SubprocessTransport struct {
	options     ClaudeAgentOptions
	isStreaming bool
	prompt      string

	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	stderr        io.ReadCloser
	ready         bool
	exitError     error
	maxBufferSize int

	mu     sync.Mutex
	closed bool
}

// NewSubprocessTransport creates a new SubprocessTransport.
// Always uses streaming mode internally (matching TypeScript/Python SDK).
func NewSubprocessTransport(prompt string, options ClaudeAgentOptions) *SubprocessTransport {
	maxBufferSize := defaultMaxBufferSize
	if options.MaxBufferSize != nil {
		maxBufferSize = *options.MaxBufferSize
	}

	return &SubprocessTransport{
		options:       options,
		isStreaming:   true, // Always streaming mode
		prompt:        prompt,
		maxBufferSize: maxBufferSize,
	}
}

// findCLI locates the Claude Code CLI binary.
func (t *SubprocessTransport) findCLI() (string, error) {
	// Check if cli_path is explicitly set
	if t.options.CLIPath != "" {
		if _, err := os.Stat(t.options.CLIPath); err == nil {
			return t.options.CLIPath, nil
		}
		return "", NewCLINotFoundError(fmt.Sprintf("Claude Code not found at: %s", t.options.CLIPath))
	}

	// Try to find claude in PATH
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}

	// Check common locations
	homeDir, _ := os.UserHomeDir()
	locations := []string{
		filepath.Join(homeDir, ".npm-global", "bin", "claude"),
		"/usr/local/bin/claude",
		filepath.Join(homeDir, ".local", "bin", "claude"),
		filepath.Join(homeDir, "node_modules", ".bin", "claude"),
		filepath.Join(homeDir, ".yarn", "bin", "claude"),
		filepath.Join(homeDir, ".claude", "local", "claude"),
	}

	for _, loc := range locations {
		if info, err := os.Stat(loc); err == nil && !info.IsDir() {
			return loc, nil
		}
	}

	return "", NewCLINotFoundError(
		"Claude Code not found. Install with:\n" +
			"  npm install -g @anthropic-ai/claude-code\n" +
			"\nIf already installed locally, try:\n" +
			"  export PATH=\"$HOME/node_modules/.bin:$PATH\"\n" +
			"\nOr provide the path via ClaudeAgentOptions:\n" +
			"  ClaudeAgentOptions{CLIPath: \"/path/to/claude\"}",
	)
}

// buildSettingsValue builds the settings value, merging sandbox settings if provided.
func (t *SubprocessTransport) buildSettingsValue() (string, error) {
	hasSettings := t.options.Settings != ""
	hasSandbox := t.options.Sandbox != nil

	if !hasSettings && !hasSandbox {
		return "", nil
	}

	// If only settings path and no sandbox, pass through as-is
	if hasSettings && !hasSandbox {
		return t.options.Settings, nil
	}

	// If we have sandbox settings, we need to merge into a JSON object
	settingsObj := make(map[string]any)

	if hasSettings {
		settingsStr := strings.TrimSpace(t.options.Settings)
		// Check if settings is a JSON string or a file path
		if strings.HasPrefix(settingsStr, "{") && strings.HasSuffix(settingsStr, "}") {
			if err := json.Unmarshal([]byte(settingsStr), &settingsObj); err != nil {
				// If parsing fails, try reading as file
				data, readErr := os.ReadFile(settingsStr)
				if readErr != nil {
					return "", fmt.Errorf("failed to parse settings: %w", err)
				}
				if err := json.Unmarshal(data, &settingsObj); err != nil {
					return "", fmt.Errorf("failed to parse settings file: %w", err)
				}
			}
		} else {
			// It's a file path
			data, err := os.ReadFile(settingsStr)
			if err != nil {
				return "", fmt.Errorf("failed to read settings file: %w", err)
			}
			if err := json.Unmarshal(data, &settingsObj); err != nil {
				return "", fmt.Errorf("failed to parse settings file: %w", err)
			}
		}
	}

	// Merge sandbox settings
	if hasSandbox {
		settingsObj["sandbox"] = t.options.Sandbox
	}

	data, err := json.Marshal(settingsObj)
	if err != nil {
		return "", fmt.Errorf("failed to marshal settings: %w", err)
	}

	return string(data), nil
}

// formatBudget formats a float64 budget value removing trailing zeros.
func formatBudget(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	return s
}

// buildCommand builds the CLI command with arguments.
func (t *SubprocessTransport) buildCommand() ([]string, error) {
	cliPath, err := t.findCLI()
	if err != nil {
		return nil, err
	}

	cmd := []string{cliPath, "--output-format", "stream-json", "--verbose"}

	// System prompt
	if t.options.SystemPrompt == nil {
		cmd = append(cmd, "--system-prompt", "")
	} else if sp, ok := t.options.SystemPrompt.(string); ok {
		cmd = append(cmd, "--system-prompt", sp)
	} else if preset, ok := t.options.SystemPrompt.(*SystemPromptPreset); ok {
		if preset.Type == "preset" && preset.Append != "" {
			cmd = append(cmd, "--append-system-prompt", preset.Append)
		}
	}

	// Handle tools option (base set of tools)
	if t.options.Tools != nil {
		switch tools := t.options.Tools.(type) {
		case []string:
			if len(tools) == 0 {
				cmd = append(cmd, "--tools", "")
			} else {
				cmd = append(cmd, "--tools", strings.Join(tools, ","))
			}
		case *ToolsPreset:
			// 'claude_code' preset maps to 'default'
			cmd = append(cmd, "--tools", "default")
		}
	}

	// Allowed tools
	if len(t.options.AllowedTools) > 0 {
		cmd = append(cmd, "--allowedTools", strings.Join(t.options.AllowedTools, ","))
	}

	// Max turns
	if t.options.MaxTurns != nil {
		cmd = append(cmd, "--max-turns", strconv.Itoa(*t.options.MaxTurns))
	}

	// Max budget (formatted without trailing zeros)
	if t.options.MaxBudgetUSD != nil {
		cmd = append(cmd, "--max-budget-usd", formatBudget(*t.options.MaxBudgetUSD))
	}

	// Disallowed tools
	if len(t.options.DisallowedTools) > 0 {
		cmd = append(cmd, "--disallowedTools", strings.Join(t.options.DisallowedTools, ","))
	}

	// Model
	if t.options.Model != "" {
		cmd = append(cmd, "--model", t.options.Model)
	}

	// Fallback model
	if t.options.FallbackModel != "" {
		cmd = append(cmd, "--fallback-model", t.options.FallbackModel)
	}

	// Betas
	if len(t.options.Betas) > 0 {
		betaStrs := make([]string, len(t.options.Betas))
		for i, b := range t.options.Betas {
			betaStrs[i] = string(b)
		}
		cmd = append(cmd, "--betas", strings.Join(betaStrs, ","))
	}

	// Permission prompt tool name
	if t.options.PermissionPromptToolName != "" {
		cmd = append(cmd, "--permission-prompt-tool", t.options.PermissionPromptToolName)
	}

	// Permission mode
	if t.options.PermissionMode != "" {
		cmd = append(cmd, "--permission-mode", string(t.options.PermissionMode))
	}

	// Continue conversation
	if t.options.ContinueConversation {
		cmd = append(cmd, "--continue")
	}

	// Resume
	if t.options.Resume != "" {
		cmd = append(cmd, "--resume", t.options.Resume)
	}

	// Settings (with sandbox merged)
	settingsValue, err := t.buildSettingsValue()
	if err != nil {
		return nil, err
	}
	if settingsValue != "" {
		cmd = append(cmd, "--settings", settingsValue)
	}

	// Add dirs
	for _, dir := range t.options.AddDirs {
		cmd = append(cmd, "--add-dir", dir)
	}

	// MCP servers (strip SDK server instances before passing to CLI)
	if len(t.options.McpServers) > 0 {
		serversForCLI := make(map[string]any)
		for name, config := range t.options.McpServers {
			if config.Type == McpServerTypeSDK {
				// For SDK servers, pass everything except what can't be serialized
				sdkConfig := map[string]any{
					"type": string(config.Type),
					"name": config.Name,
				}
				serversForCLI[name] = sdkConfig
			} else {
				serversForCLI[name] = config
			}
		}
		if len(serversForCLI) > 0 {
			mcpConfig := map[string]any{"mcpServers": serversForCLI}
			data, err := json.Marshal(mcpConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal MCP config: %w", err)
			}
			cmd = append(cmd, "--mcp-config", string(data))
		}
	} else if t.options.McpServersPath != "" {
		cmd = append(cmd, "--mcp-config", t.options.McpServersPath)
	}

	// Include partial messages
	if t.options.IncludePartialMessages {
		cmd = append(cmd, "--include-partial-messages")
	}

	// Fork session
	if t.options.ForkSession {
		cmd = append(cmd, "--fork-session")
	}

	// Agents are sent via initialize request, not CLI flag

	// Setting sources
	if len(t.options.SettingSources) > 0 {
		sources := make([]string, len(t.options.SettingSources))
		for i, s := range t.options.SettingSources {
			sources[i] = string(s)
		}
		cmd = append(cmd, "--setting-sources", strings.Join(sources, ","))
	} else {
		cmd = append(cmd, "--setting-sources", "")
	}

	// Plugins
	for _, plugin := range t.options.Plugins {
		if plugin.Type == "local" {
			cmd = append(cmd, "--plugin-dir", plugin.Path)
		}
	}

	// Extra args
	for flag, value := range t.options.ExtraArgs {
		if value == nil {
			cmd = append(cmd, "--"+flag)
		} else {
			cmd = append(cmd, "--"+flag, *value)
		}
	}

	// Resolve thinking config → --max-thinking-tokens
	// `Thinking` takes precedence over the deprecated `MaxThinkingTokens`
	resolvedMaxThinkingTokens := t.options.MaxThinkingTokens
	if t.options.Thinking != nil {
		switch tc := t.options.Thinking.(type) {
		case ThinkingConfigAdaptive:
			if resolvedMaxThinkingTokens == nil {
				v := 32000
				resolvedMaxThinkingTokens = &v
			}
		case ThinkingConfigEnabled:
			resolvedMaxThinkingTokens = &tc.BudgetTokens
		case ThinkingConfigDisabled:
			v := 0
			resolvedMaxThinkingTokens = &v
		}
	}
	if resolvedMaxThinkingTokens != nil {
		cmd = append(cmd, "--max-thinking-tokens", strconv.Itoa(*resolvedMaxThinkingTokens))
	}

	// Effort
	if t.options.Effort != nil {
		cmd = append(cmd, "--effort", *t.options.Effort)
	}

	// Output format (JSON schema)
	if t.options.OutputFormat != nil {
		if t.options.OutputFormat["type"] == "json_schema" {
			if schema, ok := t.options.OutputFormat["schema"]; ok {
				schemaData, err := json.Marshal(schema)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal JSON schema: %w", err)
				}
				cmd = append(cmd, "--json-schema", string(schemaData))
			}
		}
	}

	// Always use streaming mode with stdin (matching TypeScript/Python SDK)
	cmd = append(cmd, "--input-format", "stream-json")

	return cmd, nil
}

// Connect starts the subprocess and prepares for communication.
func (t *SubprocessTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.ready {
		return ErrAlreadyConnected
	}

	// Check Claude version (optional)
	if os.Getenv("CLAUDE_AGENT_SDK_SKIP_VERSION_CHECK") == "" {
		t.checkClaudeVersion(ctx)
	}

	cmdArgs, err := t.buildCommand()
	if err != nil {
		return err
	}

	// Build environment
	env := os.Environ()
	for k, v := range t.options.Env {
		env = append(env, k+"="+v)
	}
	env = append(env, "CLAUDE_CODE_ENTRYPOINT=sdk-go")
	env = append(env, "CLAUDE_AGENT_SDK_VERSION="+sdkVersion)

	// Enable file checkpointing if requested
	if t.options.EnableFileCheckpointing {
		env = append(env, "CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING=true")
	}

	if t.options.Cwd != "" {
		env = append(env, "PWD="+t.options.Cwd)
	}

	t.cmd = exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	t.cmd.Env = env
	if t.options.Cwd != "" {
		t.cmd.Dir = t.options.Cwd
	}

	var err2 error
	t.stdin, err2 = t.cmd.StdinPipe()
	if err2 != nil {
		return NewCLIConnectionError("failed to create stdin pipe", err2)
	}

	t.stdout, err2 = t.cmd.StdoutPipe()
	if err2 != nil {
		return NewCLIConnectionError("failed to create stdout pipe", err2)
	}

	// Setup stderr
	shouldPipeStderr := t.options.StderrCallback != nil
	if _, ok := t.options.ExtraArgs["debug-to-stderr"]; ok {
		shouldPipeStderr = true
	}

	if shouldPipeStderr {
		t.stderr, err2 = t.cmd.StderrPipe()
		if err2 != nil {
			return NewCLIConnectionError("failed to create stderr pipe", err2)
		}
		go t.handleStderr()
	}

	if err := t.cmd.Start(); err != nil {
		if t.options.Cwd != "" {
			if _, statErr := os.Stat(t.options.Cwd); os.IsNotExist(statErr) {
				return NewCLIConnectionError(fmt.Sprintf("working directory does not exist: %s", t.options.Cwd), err)
			}
		}
		return NewCLIConnectionError("failed to start Claude Code", err)
	}

	t.ready = true
	return nil
}

// handleStderr reads stderr and invokes callbacks.
func (t *SubprocessTransport) handleStderr() {
	if t.stderr == nil {
		return
	}

	scanner := bufio.NewScanner(t.stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if t.options.StderrCallback != nil {
			t.options.StderrCallback(line)
		}
	}
}

// Write sends data to the transport.
func (t *SubprocessTransport) Write(ctx context.Context, data string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.ready || t.stdin == nil {
		return NewCLIConnectionError("transport is not ready for writing", nil)
	}

	if t.cmd != nil && t.cmd.ProcessState != nil && t.cmd.ProcessState.Exited() {
		return NewCLIConnectionError(fmt.Sprintf("cannot write to terminated process (exit code: %d)", t.cmd.ProcessState.ExitCode()), nil)
	}

	if t.exitError != nil {
		return NewCLIConnectionError(fmt.Sprintf("cannot write to process that exited with error: %v", t.exitError), nil)
	}

	_, err := t.stdin.Write([]byte(data))
	if err != nil {
		t.ready = false
		t.exitError = NewCLIConnectionError("failed to write to process stdin", err)
		return t.exitError
	}

	return nil
}

// ReadMessages returns channels for messages and errors.
func (t *SubprocessTransport) ReadMessages(ctx context.Context) (<-chan RawMessage, <-chan error) {
	msgChan := make(chan RawMessage, 100)
	errChan := make(chan error, 1)

	go func() {
		defer close(msgChan)
		defer close(errChan)

		if t.stdout == nil {
			errChan <- NewCLIConnectionError("not connected", nil)
			return
		}

		scanner := bufio.NewScanner(t.stdout)
		scanner.Buffer(make([]byte, t.maxBufferSize), t.maxBufferSize)

		var jsonBuffer strings.Builder

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			// Accumulate lines and try to parse
			jsonBuffer.WriteString(line)

			if jsonBuffer.Len() > t.maxBufferSize {
				errChan <- NewJSONDecodeError(
					fmt.Sprintf("JSON message exceeded maximum buffer size of %d bytes", t.maxBufferSize),
					nil,
					jsonBuffer.String(),
				)
				jsonBuffer.Reset()
				continue
			}

			var msg RawMessage
			if err := json.Unmarshal([]byte(jsonBuffer.String()), &msg); err == nil {
				jsonBuffer.Reset()
				select {
				case msgChan <- msg:
				case <-ctx.Done():
					return
				}
			}
			// If JSON parse fails, keep accumulating (speculative parsing)
		}

		if err := scanner.Err(); err != nil {
			errChan <- NewCLIConnectionError("error reading stdout", err)
		}

		// Wait for process and check exit code
		if t.cmd != nil {
			if err := t.cmd.Wait(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					t.exitError = NewProcessError(
						fmt.Sprintf("command failed with exit code %d", exitErr.ExitCode()),
						exitErr.ExitCode(),
						"",
					)
					errChan <- t.exitError
				}
			}
		}
	}()

	return msgChan, errChan
}

// Close closes the transport and cleans up resources.
func (t *SubprocessTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true
	t.ready = false

	// Close stdin
	if t.stdin != nil {
		t.stdin.Close()
		t.stdin = nil
	}

	// Close stderr
	if t.stderr != nil {
		t.stderr.Close()
		t.stderr = nil
	}

	// Terminate process with SIGTERM (graceful shutdown)
	if t.cmd != nil && t.cmd.Process != nil {
		t.cmd.Process.Signal(syscall.SIGTERM)
		t.cmd.Wait()
	}

	// Close stdout
	if t.stdout != nil {
		t.stdout.Close()
		t.stdout = nil
	}

	return nil
}

// IsReady returns true if the transport is ready.
func (t *SubprocessTransport) IsReady() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ready
}

// EndInput closes the stdin stream.
func (t *SubprocessTransport) EndInput() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stdin != nil {
		err := t.stdin.Close()
		t.stdin = nil
		return err
	}
	return nil
}

// checkClaudeVersion checks if Claude Code version meets minimum requirements.
func (t *SubprocessTransport) checkClaudeVersion(ctx context.Context) {
	cliPath, err := t.findCLI()
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 2*1e9) // 2 seconds
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-v")
	output, err := cmd.Output()
	if err != nil {
		return
	}

	versionStr := strings.TrimSpace(string(output))
	re := regexp.MustCompile(`([0-9]+\.[0-9]+\.[0-9]+)`)
	match := re.FindStringSubmatch(versionStr)
	if len(match) < 2 {
		return
	}

	version := match[1]
	if compareVersions(version, minimumClaudeCodeVersion) < 0 {
		fmt.Fprintf(os.Stderr, "Warning: Claude Code version %s is unsupported in the Agent SDK. "+
			"Minimum required version is %s. Some features may not work correctly.\n",
			version, minimumClaudeCodeVersion)
	}
}

// compareVersions compares two semantic versions.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	for i := 0; i < 3; i++ {
		var numA, numB int
		if i < len(partsA) {
			numA, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			numB, _ = strconv.Atoi(partsB[i])
		}
		if numA < numB {
			return -1
		}
		if numA > numB {
			return 1
		}
	}
	return 0
}
