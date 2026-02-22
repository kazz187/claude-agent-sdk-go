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

	"github.com/google/jsonschema-go/jsonschema"
)

// Wire types for routing and control protocol parsing.

// MessageType represents the type field of a CLI message.
type MessageType string

const (
	MessageTypeUser                 MessageType = "user"
	MessageTypeAssistant            MessageType = "assistant"
	MessageTypeSystem               MessageType = "system"
	MessageTypeResult               MessageType = "result"
	MessageTypeStreamEvent          MessageType = "stream_event"
	MessageTypeControlResponse      MessageType = "control_response"
	MessageTypeControlRequest       MessageType = "control_request"
	MessageTypeControlCancelRequest MessageType = "control_cancel_request"
)

// MessageRole represents the role field of a message.
type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// ContentBlockType represents the type field of a content block.
type ContentBlockType string

const (
	ContentBlockTypeText       ContentBlockType = "text"
	ContentBlockTypeThinking   ContentBlockType = "thinking"
	ContentBlockTypeToolUse    ContentBlockType = "tool_use"
	ContentBlockTypeToolResult ContentBlockType = "tool_result"
)

// ControlSubtype represents the subtype field of control protocol messages.
type ControlSubtype string

const (
	// Incoming control request subtypes (from CLI)
	ControlSubtypeCanUseTool   ControlSubtype = "can_use_tool"
	ControlSubtypeHookCallback ControlSubtype = "hook_callback"
	ControlSubtypeMcpMessage   ControlSubtype = "mcp_message"

	// Outgoing control request subtypes (to CLI)
	ControlSubtypeInitialize        ControlSubtype = "initialize"
	ControlSubtypeInterrupt         ControlSubtype = "interrupt"
	ControlSubtypeSetPermissionMode ControlSubtype = "set_permission_mode"
	ControlSubtypeSetModel          ControlSubtype = "set_model"
	ControlSubtypeMcpStatus         ControlSubtype = "mcp_status"
	ControlSubtypeRewindFiles       ControlSubtype = "rewind_files"

	// Control response subtypes
	ControlSubtypeError   ControlSubtype = "error"
	ControlSubtypeSuccess ControlSubtype = "success"
)

// messageEnvelope is used to peek at the "type" field for routing.
type messageEnvelope struct {
	Type MessageType `json:"type"`
}

// controlRequestWire is the wire format for incoming control_request messages.
// Request is kept as json.RawMessage so we can peek at the subtype before
// unmarshalling into a typed struct.
type controlRequestWire struct {
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}

// Query handles bidirectional control protocol on top of Transport.
// It manages control request/response routing, hook callbacks,
// tool permission callbacks, SDK MCP servers, message streaming,
// and initialization handshake.
type Query struct {
	transport         Transport
	isStreamingMode   bool
	canUseTool        CanUseToolFunc
	hooks             map[HookEvent][]*HookMatcher
	sdkMcpServers     map[string]*SdkMcpServer
	agents            map[string]*AgentDefinition
	initializeTimeout time.Duration

	// Control protocol state
	pendingResponses map[string]chan *controlResponsePayload
	hookCallbacks    map[string]HookCallback
	nextCallbackID   int64
	requestCounter   int64

	// Message stream
	messageChan chan json.RawMessage
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
	initializationResult json.RawMessage

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// QueryOptions contains options for creating a Query.
type QueryOptions struct {
	CanUseTool        CanUseToolFunc
	Hooks             map[HookEvent][]*HookMatcher
	SdkMcpServers     map[string]*SdkMcpServer
	Agents            map[string]*AgentDefinition
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
		pendingResponses:   make(map[string]chan *controlResponsePayload),
		hookCallbacks:      make(map[string]HookCallback),
		messageChan:        make(chan json.RawMessage, 100),
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

	q.wg.Go(func() {
		q.readMessages(ctx, msgChan, errChan)
	})

	return nil
}

// readMessages reads messages from transport and routes them.
func (q *Query) readMessages(ctx context.Context, msgChan <-chan json.RawMessage, errChan <-chan error) {
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

			var envelope messageEnvelope
			if err := json.Unmarshal(msg, &envelope); err != nil {
				continue
			}

			switch envelope.Type {
			case MessageTypeControlResponse:
				q.handleControlResponse(msg)
			case MessageTypeControlRequest:
				// Handle control request in a separate goroutine to avoid blocking reader
				go q.handleControlRequest(msg)
			case MessageTypeControlCancelRequest:
				// TODO: Implement cancellation support
			default:
				// Track results for proper stream closure
				if envelope.Type == MessageTypeResult {
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
		select {
		case ch <- &controlResponsePayload{
			Subtype:   ControlSubtypeError,
			RequestID: requestID,
			Error:     err.Error(),
		}:
		default:
		}
		delete(q.pendingResponses, requestID)
	}
}

// handleControlResponse handles incoming control response from CLI.
func (q *Query) handleControlResponse(msg json.RawMessage) {
	var wire controlResponseMessage
	if err := json.Unmarshal(msg, &wire); err != nil {
		return
	}

	if wire.Response.RequestID == "" {
		return
	}

	q.mu.Lock()
	ch, ok := q.pendingResponses[wire.Response.RequestID]
	if ok {
		delete(q.pendingResponses, wire.Response.RequestID)
	}
	q.mu.Unlock()

	if ok && ch != nil {
		payload := wire.Response
		select {
		case ch <- &payload:
		default:
		}
	}
}

// handleControlRequest handles incoming control request from CLI.
func (q *Query) handleControlRequest(msg json.RawMessage) {
	var wire controlRequestWire
	if err := json.Unmarshal(msg, &wire); err != nil {
		return
	}
	if wire.RequestID == "" || wire.Request == nil {
		return
	}

	// Peek at the subtype
	var envelope subtypeEnvelope
	if err := json.Unmarshal(wire.Request, &envelope); err != nil {
		return
	}

	var responseData any
	var err error

	switch envelope.Subtype {
	case ControlSubtypeCanUseTool:
		responseData, err = q.handleCanUseTool(wire.Request)
	case ControlSubtypeHookCallback:
		responseData, err = q.handleHookCallback(wire.Request)
	case ControlSubtypeMcpMessage:
		responseData, err = q.handleMcpMessage(wire.Request)
	default:
		err = fmt.Errorf("unsupported control request subtype: %s", envelope.Subtype)
	}

	// Send response
	var response controlResponseMessage
	if err != nil {
		response = controlResponseMessage{
			Type: MessageTypeControlResponse,
			Response: controlResponsePayload{
				Subtype:   ControlSubtypeError,
				RequestID: wire.RequestID,
				Error:     err.Error(),
			},
		}
	} else {
		rawResponse, _ := json.Marshal(responseData)
		response = controlResponseMessage{
			Type: MessageTypeControlResponse,
			Response: controlResponsePayload{
				Subtype:   ControlSubtypeSuccess,
				RequestID: wire.RequestID,
				Response:  rawResponse,
			},
		}
	}

	data, _ := json.Marshal(response)
	q.transport.Write(q.ctx, string(data)+"\n")
}

// handleCanUseTool handles tool permission requests.
func (q *Query) handleCanUseTool(raw json.RawMessage) (any, error) {
	if q.canUseTool == nil {
		return nil, fmt.Errorf("canUseTool callback is not provided")
	}

	var req canUseToolRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal can_use_tool request: %w", err)
	}

	ctx := ToolPermissionContext{
		Signal:      q.ctx,
		Suggestions: req.PermissionSuggestions,
	}

	result, err := q.canUseTool(req.ToolName, req.Input, ctx)
	if err != nil {
		return nil, err
	}

	switch r := result.(type) {
	case PermissionResultAllow:
		r.Behavior = PermissionBehaviorAllow
		if r.UpdatedInput == nil {
			r.UpdatedInput = req.Input
		}
		return r, nil

	case PermissionResultDeny:
		r.Behavior = PermissionBehaviorDeny
		return r, nil

	default:
		return nil, fmt.Errorf("invalid permission result type")
	}
}

// handleHookCallback handles hook callback requests.
func (q *Query) handleHookCallback(raw json.RawMessage) (any, error) {
	var req hookCallbackRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal hook_callback request: %w", err)
	}

	q.mu.Lock()
	callback, ok := q.hookCallbacks[req.CallbackID]
	q.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("no hook callback found for ID: %s", req.CallbackID)
	}

	ctx := HookContext{
		Signal: q.ctx,
	}

	output, err := callback(req.Input, req.ToolUseID, ctx)
	if err != nil {
		return nil, err
	}

	return output, nil
}

// handleMcpMessage handles SDK MCP server requests.
func (q *Query) handleMcpMessage(raw json.RawMessage) (any, error) {
	var req mcpMessageRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("failed to unmarshal mcp_message request: %w", err)
	}

	if req.ServerName == "" {
		return nil, fmt.Errorf("missing server_name for MCP request")
	}

	resp := q.handleSdkMcpRequest(req.ServerName, &req.Message)
	return mcpMessageResponse{McpResponse: resp}, nil
}

func newJSONRPCResult(id any, result any) *jsonrpcResponse {
	return &jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func newJSONRPCError(id any, code int, msg string) *jsonrpcResponse {
	return &jsonrpcResponse{JSONRPC: "2.0", ID: id, Error: &jsonrpcError{Code: code, Message: msg}}
}

// handleSdkMcpRequest handles an MCP JSONRPC request for an SDK server.
func (q *Query) handleSdkMcpRequest(serverName string, req *jsonrpcRequest) *jsonrpcResponse {
	if q.sdkMcpServers == nil {
		return newJSONRPCError(req.ID, -32601, fmt.Sprintf("Server '%s' not found", serverName))
	}

	server, ok := q.sdkMcpServers[serverName]
	if !ok {
		return newJSONRPCError(req.ID, -32601, fmt.Sprintf("Server '%s' not found", serverName))
	}

	switch req.Method {
	case jsonrpcMethodInitialize:
		return newJSONRPCResult(req.ID, mcpInitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities:    mcpCapabilities{Tools: map[string]any{}},
			ServerInfo:      mcpServerInfo{Name: server.Name, Version: server.Version},
		})

	case jsonrpcMethodToolsList:
		tools := server.GetTools()
		toolsData := make([]mcpToolInfo, len(tools))
		for i, tool := range tools {
			info := mcpToolInfo{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			}
			if info.InputSchema == nil {
				info.InputSchema = &jsonschema.Schema{}
			}
			if tool.Annotations != nil && tool.Annotations.hasValue() {
				info.Annotations = tool.Annotations
			}
			toolsData[i] = info
		}
		return newJSONRPCResult(req.ID, mcpToolsListResult{Tools: toolsData})

	case jsonrpcMethodToolsCall:
		var params mcpToolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return newJSONRPCError(req.ID, -32602, "invalid params for tools/call")
		}
		toolName := params.Name
		arguments := params.Arguments

		tool := server.FindTool(toolName)
		if tool == nil {
			return newJSONRPCError(req.ID, -32601, fmt.Sprintf("Tool '%s' not found", toolName))
		}

		if tool.Handler == nil {
			return newJSONRPCError(req.ID, -32603, fmt.Sprintf("Tool '%s' has no handler", toolName))
		}

		content, err := tool.Handler(arguments)
		if err != nil {
			return newJSONRPCResult(req.ID, mcpToolCallResult{
				Content: []map[string]any{{"type": "text", "text": err.Error()}},
				IsError: true,
			})
		}

		return newJSONRPCResult(req.ID, mcpToolCallResult{Content: content})

	case jsonrpcMethodNotificationsInitialized:
		return newJSONRPCResult(nil, struct{}{})

	default:
		return newJSONRPCError(req.ID, -32601, fmt.Sprintf("Method '%s' not found", req.Method))
	}
}

// sendControlRequest sends a control request to CLI and waits for response.
func (q *Query) sendControlRequest(request any, timeout time.Duration) (json.RawMessage, error) {
	if !q.isStreamingMode {
		return nil, fmt.Errorf("control requests require streaming mode")
	}

	// Generate unique request ID
	counter := atomic.AddInt64(&q.requestCounter, 1)
	randBytes := make([]byte, 4)
	rand.Read(randBytes)
	requestID := fmt.Sprintf("req_%d_%s", counter, hex.EncodeToString(randBytes))

	// Create response channel
	responseChan := make(chan *controlResponsePayload, 1)
	q.mu.Lock()
	q.pendingResponses[requestID] = responseChan
	q.mu.Unlock()

	// Build and send request
	controlRequest := controlRequestMessage{
		Type:      MessageTypeControlRequest,
		RequestID: requestID,
		Request:   request,
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
	case payload := <-responseChan:
		if payload.Subtype == ControlSubtypeError {
			return nil, fmt.Errorf("control request error: %s", payload.Error)
		}
		return payload.Response, nil

	case <-ctx.Done():
		q.mu.Lock()
		delete(q.pendingResponses, requestID)
		q.mu.Unlock()
		return nil, fmt.Errorf("control request timeout")
	}
}

// Initialize initializes the control protocol if in streaming mode.
func (q *Query) Initialize() (json.RawMessage, error) {
	if !q.isStreamingMode {
		return nil, nil
	}

	// Build hooks configuration for initialization
	hooksConfig := make(map[string][]hookMatcherConfig)
	if q.hooks != nil {
		for event, matchers := range q.hooks {
			if len(matchers) == 0 {
				continue
			}

			configs := make([]hookMatcherConfig, 0, len(matchers))
			for _, matcher := range matchers {
				callbackIDs := make([]string, 0, len(matcher.Hooks))
				for _, callback := range matcher.Hooks {
					callbackID := fmt.Sprintf("hook_%d", atomic.AddInt64(&q.nextCallbackID, 1))
					q.mu.Lock()
					q.hookCallbacks[callbackID] = callback
					q.mu.Unlock()
					callbackIDs = append(callbackIDs, callbackID)
				}

				configs = append(configs, hookMatcherConfig{
					Matcher:         matcher.Matcher,
					HookCallbackIDs: callbackIDs,
					Timeout:         matcher.Timeout,
				})
			}
			hooksConfig[string(event)] = configs
		}
	}

	req := initializeRequest{
		Subtype: ControlSubtypeInitialize,
		Hooks:   hooksConfig,
	}
	if q.agents != nil {
		req.Agents = q.agents
	}

	response, err := q.sendControlRequest(req, q.initializeTimeout)
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
	_, err := q.sendControlRequest(interruptRequest{Subtype: ControlSubtypeInterrupt}, 60*time.Second)
	return err
}

// SetPermissionMode changes the permission mode.
func (q *Query) SetPermissionMode(mode string) error {
	_, err := q.sendControlRequest(setPermissionModeRequest{
		Subtype: ControlSubtypeSetPermissionMode,
		Mode:    mode,
	}, 60*time.Second)
	return err
}

// SetModel changes the AI model. Pass nil to use default.
func (q *Query) SetModel(model *string) error {
	var m any
	if model != nil {
		m = *model
	}
	_, err := q.sendControlRequest(setModelRequest{
		Subtype: ControlSubtypeSetModel,
		Model:   m,
	}, 60*time.Second)
	return err
}

// GetMcpStatus gets current MCP server connection status.
func (q *Query) GetMcpStatus() (json.RawMessage, error) {
	return q.sendControlRequest(mcpStatusRequest{Subtype: ControlSubtypeMcpStatus}, 60*time.Second)
}

// RewindFiles rewinds tracked files to their state at a specific user message.
func (q *Query) RewindFiles(userMessageID string) error {
	_, err := q.sendControlRequest(rewindFilesRequest{
		Subtype:       ControlSubtypeRewindFiles,
		UserMessageID: userMessageID,
	}, 60*time.Second)
	return err
}

// ReceiveMessages returns a channel for receiving SDK messages.
func (q *Query) ReceiveMessages() <-chan json.RawMessage {
	return q.messageChan
}

// Errors returns a channel for receiving errors.
func (q *Query) Errors() <-chan error {
	return q.errorChan
}

// GetInitializationResult returns the initialization result.
func (q *Query) GetInitializationResult() json.RawMessage {
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
