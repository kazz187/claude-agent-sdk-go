package claudeagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Query handles bidirectional control protocol on top of Transport.
// It manages control request/response routing, hook callbacks,
// tool permission callbacks, SDK MCP servers, message streaming,
// and initialization handshake.
type Query struct {
	transport         Transport
	isStreamingMode   bool
	canUseTool        CanUseToolFunc
	hooks             map[HookEvent][]HookMatcher
	sdkMcpServers     map[string]*SdkMcpServer
	agents            map[string]map[string]any
	initializeTimeout time.Duration

	// Control protocol state
	pendingResponses map[string]chan map[string]any
	hookCallbacks    map[string]HookCallback
	nextCallbackID   int64
	requestCounter   int64

	// Message stream
	messageChan chan RawMessage
	errorChan   chan error

	// Track first result for proper stream closure with SDK MCP servers
	firstResultOnce sync.Once
	firstResultChan chan struct{}

	// Stream close timeout
	streamCloseTimeout time.Duration

	// State
	mu                   sync.Mutex
	initialized          bool
	closed               bool
	initializationResult map[string]any

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// QueryOptions contains options for creating a Query.
type QueryOptions struct {
	CanUseTool        CanUseToolFunc
	Hooks             map[HookEvent][]HookMatcher
	SdkMcpServers     map[string]*SdkMcpServer
	Agents            map[string]map[string]any
	InitializeTimeout time.Duration
}

// NewQuery creates a new Query.
func NewQuery(transport Transport, isStreamingMode bool, options QueryOptions) *Query {
	initTimeout := options.InitializeTimeout
	if initTimeout == 0 {
		initTimeout = 60 * time.Second
	}

	// Parse stream close timeout from environment
	streamCloseTimeout := 60 * time.Second
	if timeoutStr := os.Getenv("CLAUDE_CODE_STREAM_CLOSE_TIMEOUT"); timeoutStr != "" {
		if ms, err := strconv.ParseInt(timeoutStr, 10, 64); err == nil {
			streamCloseTimeout = time.Duration(ms) * time.Millisecond
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	q := &Query{
		transport:          transport,
		isStreamingMode:    isStreamingMode,
		canUseTool:         options.CanUseTool,
		hooks:              options.Hooks,
		sdkMcpServers:      options.SdkMcpServers,
		agents:             options.Agents,
		initializeTimeout:  initTimeout,
		pendingResponses:   make(map[string]chan map[string]any),
		hookCallbacks:      make(map[string]HookCallback),
		messageChan:        make(chan RawMessage, 100),
		errorChan:          make(chan error, 1),
		firstResultChan:    make(chan struct{}),
		streamCloseTimeout: streamCloseTimeout,
		ctx:                ctx,
		cancel:             cancel,
	}

	return q
}

// Start begins reading messages from transport.
func (q *Query) Start(ctx context.Context) error {
	msgChan, errChan := q.transport.ReadMessages(ctx)

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		q.readMessages(ctx, msgChan, errChan)
	}()

	return nil
}

// readMessages reads messages from transport and routes them.
func (q *Query) readMessages(ctx context.Context, msgChan <-chan RawMessage, errChan <-chan error) {
	defer close(q.messageChan)

	for {
		select {
		case <-ctx.Done():
			q.failPendingRequests(ctx.Err())
			return
		case <-q.ctx.Done():
			q.failPendingRequests(q.ctx.Err())
			return
		case err, ok := <-errChan:
			if ok && err != nil {
				q.failPendingRequests(err)
				select {
				case q.errorChan <- err:
				default:
				}
			}
		case msg, ok := <-msgChan:
			if !ok {
				q.failPendingRequests(fmt.Errorf("transport closed"))
				return
			}

			q.mu.Lock()
			if q.closed {
				q.mu.Unlock()
				return
			}
			q.mu.Unlock()

			msgType, _ := msg["type"].(string)

			switch msgType {
			case "control_response":
				q.handleControlResponse(msg)
			case "control_request":
				// Handle control request in a separate goroutine to avoid blocking reader
				go q.handleControlRequest(msg)
			case "control_cancel_request":
				// TODO: Implement cancellation support
			default:
				// Track results for proper stream closure
				if msgType == "result" {
					q.firstResultOnce.Do(func() {
						close(q.firstResultChan)
					})
				}

				// Regular SDK messages go to the stream
				select {
				case q.messageChan <- msg:
				case <-ctx.Done():
					return
				case <-q.ctx.Done():
					return
				}
			}
		}
	}
}

// failPendingRequests sends error to all pending control requests.
func (q *Query) failPendingRequests(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for requestID, ch := range q.pendingResponses {
		errResponse := map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      err.Error(),
		}
		select {
		case ch <- errResponse:
		default:
		}
		delete(q.pendingResponses, requestID)
	}
}

// handleControlResponse handles incoming control response from CLI.
func (q *Query) handleControlResponse(msg RawMessage) {
	response, _ := msg["response"].(map[string]any)
	if response == nil {
		return
	}

	requestID, _ := response["request_id"].(string)
	if requestID == "" {
		return
	}

	q.mu.Lock()
	ch, ok := q.pendingResponses[requestID]
	if ok {
		delete(q.pendingResponses, requestID)
	}
	q.mu.Unlock()

	if ok && ch != nil {
		select {
		case ch <- response:
		default:
		}
	}
}

// handleControlRequest handles incoming control request from CLI.
func (q *Query) handleControlRequest(msg RawMessage) {
	requestID, _ := msg["request_id"].(string)
	requestData, _ := msg["request"].(map[string]any)
	if requestID == "" || requestData == nil {
		return
	}

	subtype, _ := requestData["subtype"].(string)

	var responseData map[string]any
	var err error

	switch subtype {
	case "can_use_tool":
		responseData, err = q.handleCanUseTool(requestData)
	case "hook_callback":
		responseData, err = q.handleHookCallback(requestData)
	case "mcp_message":
		responseData, err = q.handleMcpMessage(requestData)
	default:
		err = fmt.Errorf("unsupported control request subtype: %s", subtype)
	}

	// Send response
	var response map[string]any
	if err != nil {
		response = map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "error",
				"request_id": requestID,
				"error":      err.Error(),
			},
		}
	} else {
		response = map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": requestID,
				"response":   responseData,
			},
		}
	}

	data, _ := json.Marshal(response)
	q.transport.Write(q.ctx, string(data)+"\n")
}

// handleCanUseTool handles tool permission requests.
func (q *Query) handleCanUseTool(requestData map[string]any) (map[string]any, error) {
	if q.canUseTool == nil {
		return nil, fmt.Errorf("canUseTool callback is not provided")
	}

	toolName, _ := requestData["tool_name"].(string)
	input, _ := requestData["input"].(map[string]any)
	suggestions, _ := requestData["permission_suggestions"].([]any)

	// Convert suggestions
	var permSuggestions []PermissionUpdate
	for _, s := range suggestions {
		if sMap, ok := s.(map[string]any); ok {
			pu := PermissionUpdate{}
			if t, ok := sMap["type"].(string); ok {
				pu.Type = PermissionUpdateType(t)
			}
			permSuggestions = append(permSuggestions, pu)
		}
	}

	ctx := ToolPermissionContext{
		Signal:      q.ctx,
		Suggestions: permSuggestions,
	}

	result, err := q.canUseTool(toolName, input, ctx)
	if err != nil {
		return nil, err
	}

	switch r := result.(type) {
	case PermissionResultAllow:
		responseData := map[string]any{
			"behavior": "allow",
		}
		if r.UpdatedInput != nil {
			responseData["updatedInput"] = r.UpdatedInput
		} else {
			responseData["updatedInput"] = input
		}
		if len(r.UpdatedPermissions) > 0 {
			perms := make([]map[string]any, len(r.UpdatedPermissions))
			for i, p := range r.UpdatedPermissions {
				perms[i] = p.ToMap()
			}
			responseData["updatedPermissions"] = perms
		}
		return responseData, nil

	case PermissionResultDeny:
		responseData := map[string]any{
			"behavior": "deny",
			"message":  r.Message,
		}
		if r.Interrupt {
			responseData["interrupt"] = true
		}
		return responseData, nil

	default:
		return nil, fmt.Errorf("invalid permission result type")
	}
}

// handleHookCallback handles hook callback requests.
func (q *Query) handleHookCallback(requestData map[string]any) (map[string]any, error) {
	callbackID, _ := requestData["callback_id"].(string)
	inputData, _ := requestData["input"].(map[string]any)
	toolUseID, _ := requestData["tool_use_id"].(string)

	q.mu.Lock()
	callback, ok := q.hookCallbacks[callbackID]
	q.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("no hook callback found for ID: %s", callbackID)
	}

	// Convert input to HookInput
	input := HookInput{}
	if v, ok := inputData["session_id"].(string); ok {
		input.SessionID = v
	}
	if v, ok := inputData["transcript_path"].(string); ok {
		input.TranscriptPath = v
	}
	if v, ok := inputData["cwd"].(string); ok {
		input.Cwd = v
	}
	if v, ok := inputData["permission_mode"].(string); ok {
		input.PermissionMode = v
	}
	if v, ok := inputData["hook_event_name"].(string); ok {
		input.HookEventName = HookEvent(v)
	}
	if v, ok := inputData["tool_name"].(string); ok {
		input.ToolName = v
	}
	if v, ok := inputData["tool_input"].(map[string]any); ok {
		input.ToolInput = v
	}
	if v, ok := inputData["tool_response"]; ok {
		input.ToolResponse = v
	}
	if v, ok := inputData["tool_use_id"].(string); ok {
		input.ToolUseID = v
	}
	if v, ok := inputData["error"].(string); ok {
		input.Error = v
	}
	if v, ok := inputData["is_interrupt"].(bool); ok {
		input.IsInterrupt = v
	}
	if v, ok := inputData["prompt"].(string); ok {
		input.Prompt = v
	}
	if v, ok := inputData["stop_hook_active"].(bool); ok {
		input.StopHookActive = v
	}
	if v, ok := inputData["trigger"].(string); ok {
		input.Trigger = v
	}
	if v, ok := inputData["custom_instructions"].(string); ok {
		input.CustomInstructions = v
	}
	if v, ok := inputData["agent_id"].(string); ok {
		input.AgentID = v
	}
	if v, ok := inputData["agent_transcript_path"].(string); ok {
		input.AgentTranscriptPath = v
	}
	if v, ok := inputData["agent_type"].(string); ok {
		input.AgentType = v
	}
	if v, ok := inputData["message"].(string); ok {
		input.Message = v
	}
	if v, ok := inputData["title"].(string); ok {
		input.Title = v
	}
	if v, ok := inputData["notification_type"].(string); ok {
		input.NotificationType = v
	}
	if v, ok := inputData["permission_suggestions"].([]any); ok {
		input.PermissionSuggestions = v
	}

	ctx := HookContext{
		Signal: q.ctx,
	}

	output, err := callback(input, toolUseID, ctx)
	if err != nil {
		return nil, err
	}

	// Convert output to map
	result := make(map[string]any)
	if output.Async != nil {
		result["async"] = *output.Async
	}
	if output.AsyncTimeout != nil {
		result["asyncTimeout"] = *output.AsyncTimeout
	}
	if output.Continue != nil {
		result["continue"] = *output.Continue
	}
	if output.SuppressOutput {
		result["suppressOutput"] = true
	}
	if output.StopReason != "" {
		result["stopReason"] = output.StopReason
	}
	if output.Decision != "" {
		result["decision"] = output.Decision
	}
	if output.SystemMessage != "" {
		result["systemMessage"] = output.SystemMessage
	}
	if output.Reason != "" {
		result["reason"] = output.Reason
	}
	if output.HookSpecificOutput != nil {
		result["hookSpecificOutput"] = output.HookSpecificOutput
	}

	return result, nil
}

// handleMcpMessage handles SDK MCP server requests.
func (q *Query) handleMcpMessage(requestData map[string]any) (map[string]any, error) {
	serverName, _ := requestData["server_name"].(string)
	message, _ := requestData["message"].(map[string]any)

	if serverName == "" || message == nil {
		return nil, fmt.Errorf("missing server_name or message for MCP request")
	}

	mcpResponse := q.handleSdkMcpRequest(serverName, message)
	return map[string]any{"mcp_response": mcpResponse}, nil
}

// handleSdkMcpRequest handles an MCP JSONRPC request for an SDK server.
func (q *Query) handleSdkMcpRequest(serverName string, message map[string]any) map[string]any {
	if q.sdkMcpServers == nil {
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      message["id"],
			"error": map[string]any{
				"code":    -32601,
				"message": fmt.Sprintf("Server '%s' not found", serverName),
			},
		}
	}

	server, ok := q.sdkMcpServers[serverName]
	if !ok {
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      message["id"],
			"error": map[string]any{
				"code":    -32601,
				"message": fmt.Sprintf("Server '%s' not found", serverName),
			},
		}
	}

	method, _ := message["method"].(string)
	params, _ := message["params"].(map[string]any)

	switch method {
	case "initialize":
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      message["id"],
			"result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    server.Name,
					"version": server.Version,
				},
			},
		}

	case "tools/list":
		tools := server.GetTools()
		toolsData := make([]map[string]any, len(tools))
		for i, tool := range tools {
			toolData := map[string]any{
				"name": tool.Name,
			}
			if tool.Description != "" {
				toolData["description"] = tool.Description
			}
			if tool.InputSchema != nil {
				toolData["inputSchema"] = tool.InputSchema
			} else {
				toolData["inputSchema"] = map[string]any{}
			}
			if tool.Annotations != nil {
				annots := map[string]any{}
				if tool.Annotations.ReadOnlyHint != nil {
					annots["readOnlyHint"] = *tool.Annotations.ReadOnlyHint
				}
				if tool.Annotations.DestructiveHint != nil {
					annots["destructiveHint"] = *tool.Annotations.DestructiveHint
				}
				if tool.Annotations.IdempotentHint != nil {
					annots["idempotentHint"] = *tool.Annotations.IdempotentHint
				}
				if tool.Annotations.OpenWorldHint != nil {
					annots["openWorldHint"] = *tool.Annotations.OpenWorldHint
				}
				if len(annots) > 0 {
					toolData["annotations"] = annots
				}
			}
			toolsData[i] = toolData
		}
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      message["id"],
			"result":  map[string]any{"tools": toolsData},
		}

	case "tools/call":
		toolName, _ := params["name"].(string)
		arguments, _ := params["arguments"].(map[string]any)

		tool := server.FindTool(toolName)
		if tool == nil {
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      message["id"],
				"error": map[string]any{
					"code":    -32601,
					"message": fmt.Sprintf("Tool '%s' not found", toolName),
				},
			}
		}

		if tool.Handler == nil {
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      message["id"],
				"error": map[string]any{
					"code":    -32603,
					"message": fmt.Sprintf("Tool '%s' has no handler", toolName),
				},
			}
		}

		content, err := tool.Handler(arguments)
		if err != nil {
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      message["id"],
				"result": map[string]any{
					"content":  []map[string]any{{"type": "text", "text": err.Error()}},
					"is_error": true,
				},
			}
		}

		return map[string]any{
			"jsonrpc": "2.0",
			"id":      message["id"],
			"result":  map[string]any{"content": content},
		}

	case "notifications/initialized":
		return map[string]any{"jsonrpc": "2.0", "result": map[string]any{}}

	default:
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      message["id"],
			"error": map[string]any{
				"code":    -32601,
				"message": fmt.Sprintf("Method '%s' not found", method),
			},
		}
	}
}

// sendControlRequest sends a control request to CLI and waits for response.
func (q *Query) sendControlRequest(request map[string]any, timeout time.Duration) (map[string]any, error) {
	if !q.isStreamingMode {
		return nil, fmt.Errorf("control requests require streaming mode")
	}

	// Generate unique request ID
	counter := atomic.AddInt64(&q.requestCounter, 1)
	randBytes := make([]byte, 4)
	rand.Read(randBytes)
	requestID := fmt.Sprintf("req_%d_%s", counter, hex.EncodeToString(randBytes))

	// Create response channel
	responseChan := make(chan map[string]any, 1)
	q.mu.Lock()
	q.pendingResponses[requestID] = responseChan
	q.mu.Unlock()

	// Build and send request
	controlRequest := map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request":    request,
	}

	data, err := json.Marshal(controlRequest)
	if err != nil {
		q.mu.Lock()
		delete(q.pendingResponses, requestID)
		q.mu.Unlock()
		return nil, fmt.Errorf("failed to marshal control request: %w", err)
	}

	if err := q.transport.Write(q.ctx, string(data)+"\n"); err != nil {
		q.mu.Lock()
		delete(q.pendingResponses, requestID)
		q.mu.Unlock()
		return nil, err
	}

	// Wait for response with timeout
	ctx, cancel := context.WithTimeout(q.ctx, timeout)
	defer cancel()

	select {
	case response := <-responseChan:
		subtype, _ := response["subtype"].(string)
		if subtype == "error" {
			errMsg, _ := response["error"].(string)
			return nil, fmt.Errorf("control request error: %s", errMsg)
		}
		responseData, _ := response["response"].(map[string]any)
		return responseData, nil

	case <-ctx.Done():
		q.mu.Lock()
		delete(q.pendingResponses, requestID)
		q.mu.Unlock()
		return nil, fmt.Errorf("control request timeout: %v", request["subtype"])
	}
}

// Initialize initializes the control protocol if in streaming mode.
func (q *Query) Initialize() (map[string]any, error) {
	if !q.isStreamingMode {
		return nil, nil
	}

	// Build hooks configuration for initialization
	hooksConfig := make(map[string]any)
	if q.hooks != nil {
		for event, matchers := range q.hooks {
			if len(matchers) == 0 {
				continue
			}

			matcherConfigs := make([]map[string]any, 0, len(matchers))
			for _, matcher := range matchers {
				callbackIDs := make([]string, 0, len(matcher.Hooks))
				for _, callback := range matcher.Hooks {
					callbackID := fmt.Sprintf("hook_%d", atomic.AddInt64(&q.nextCallbackID, 1))
					q.mu.Lock()
					q.hookCallbacks[callbackID] = callback
					q.mu.Unlock()
					callbackIDs = append(callbackIDs, callbackID)
				}

				matcherConfig := map[string]any{
					"matcher":         matcher.Matcher,
					"hookCallbackIds": callbackIDs,
				}
				if matcher.Timeout > 0 {
					matcherConfig["timeout"] = matcher.Timeout
				}
				matcherConfigs = append(matcherConfigs, matcherConfig)
			}
			hooksConfig[string(event)] = matcherConfigs
		}
	}

	// Send initialize request
	request := map[string]any{
		"subtype": "initialize",
	}
	if len(hooksConfig) > 0 {
		request["hooks"] = hooksConfig
	} else {
		request["hooks"] = nil
	}
	if q.agents != nil {
		request["agents"] = q.agents
	}

	response, err := q.sendControlRequest(request, q.initializeTimeout)
	if err != nil {
		return nil, err
	}

	q.mu.Lock()
	q.initialized = true
	q.initializationResult = response
	q.mu.Unlock()

	return response, nil
}

// Interrupt sends an interrupt control request.
func (q *Query) Interrupt() error {
	_, err := q.sendControlRequest(map[string]any{"subtype": "interrupt"}, 60*time.Second)
	return err
}

// SetPermissionMode changes the permission mode.
func (q *Query) SetPermissionMode(mode string) error {
	_, err := q.sendControlRequest(map[string]any{
		"subtype": "set_permission_mode",
		"mode":    mode,
	}, 60*time.Second)
	return err
}

// SetModel changes the AI model. Pass nil to use default.
func (q *Query) SetModel(model *string) error {
	request := map[string]any{
		"subtype": "set_model",
	}
	if model != nil {
		request["model"] = *model
	} else {
		// Explicitly send null to reset to default
		request["model"] = nil
	}
	_, err := q.sendControlRequest(request, 60*time.Second)
	return err
}

// GetMcpStatus gets current MCP server connection status.
func (q *Query) GetMcpStatus() (map[string]any, error) {
	return q.sendControlRequest(map[string]any{"subtype": "mcp_status"}, 60*time.Second)
}

// RewindFiles rewinds tracked files to their state at a specific user message.
func (q *Query) RewindFiles(userMessageID string) error {
	_, err := q.sendControlRequest(map[string]any{
		"subtype":         "rewind_files",
		"user_message_id": userMessageID,
	}, 60*time.Second)
	return err
}

// ReceiveMessages returns a channel for receiving SDK messages.
func (q *Query) ReceiveMessages() <-chan RawMessage {
	return q.messageChan
}

// Errors returns a channel for receiving errors.
func (q *Query) Errors() <-chan error {
	return q.errorChan
}

// GetInitializationResult returns the initialization result.
func (q *Query) GetInitializationResult() map[string]any {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.initializationResult
}

// StreamInput streams input messages to transport.
// If SDK MCP servers or hooks are present, waits for the first result
// before closing stdin to allow bidirectional control protocol communication.
func (q *Query) StreamInput(ctx context.Context, messages <-chan map[string]any) error {
	for {
		select {
		case <-ctx.Done():
			return q.endInputWithWait()
		case <-q.ctx.Done():
			return q.endInputWithWait()
		case msg, ok := <-messages:
			if !ok {
				return q.endInputWithWait()
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			if err := q.transport.Write(ctx, string(data)+"\n"); err != nil {
				return err
			}
		}
	}
}

// endInputWithWait waits for first result if SDK MCP servers or hooks are present
// before ending input.
func (q *Query) endInputWithWait() error {
	hasHooks := len(q.hooks) > 0
	hasSdkMcp := len(q.sdkMcpServers) > 0

	if hasSdkMcp || hasHooks {
		// Wait for first result before closing stdin
		timer := time.NewTimer(q.streamCloseTimeout)
		defer timer.Stop()
		select {
		case <-q.firstResultChan:
		case <-timer.C:
		case <-q.ctx.Done():
		}
	}

	return q.transport.EndInput()
}

// Close closes the query and transport.
func (q *Query) Close() error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	q.mu.Unlock()

	q.cancel()
	q.wg.Wait()

	return q.transport.Close()
}
