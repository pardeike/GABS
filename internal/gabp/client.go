package gabp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	gabpruntime "github.com/pardeike/gabp-runtime/runtime"
	"github.com/pardeike/gabs/internal/util"
	"github.com/pardeike/gabs/internal/version"
)

// Client speaks GABP over TCP NDJSON.
type Client struct {
	conn           net.Conn
	writer         *util.LSPFrameWriter
	reader         *util.LSPFrameReader
	token          string
	agentId        string
	capabilities   Capabilities
	pendingReqs    map[string]chan *util.GABPMessage
	mu             sync.RWMutex
	log            util.Logger
	eventHandlers  map[string][]EventHandler
	sequences      map[string]int
	connected      bool
	disconnected   chan struct{}
	disconnectErr  error
	disconnectOnce sync.Once
	onDisconnect   func(error)
}

// EventHandler is a function that handles events
type EventHandler func(channel string, seq int, payload interface{})

var (
	ErrClientNotConnected = errors.New("GABP client is not connected")
	ErrClientClosed       = errors.New("GABP client connection closed")
)

type Capabilities = gabpruntime.Capabilities
type Limits = gabpruntime.Limits
type SessionHelloParams = gabpruntime.SessionHelloParams
type ClientInfo = gabpruntime.ClientInfo
type SessionWelcomeResult = gabpruntime.SessionWelcomeResult
type AppInfo = gabpruntime.AppInfo
type ServerInfo = gabpruntime.ServerInfo

// NewClient creates a new GABP client
func NewClient(log util.Logger) *Client {
	// Seed the global random number generator for backoff jitter
	// Use current time with nanosecond precision to avoid identical seeds
	rand.Seed(time.Now().UnixNano())

	return &Client{
		pendingReqs:   make(map[string]chan *util.GABPMessage),
		eventHandlers: make(map[string][]EventHandler),
		sequences:     make(map[string]int),
		log:           log,
		disconnected:  make(chan struct{}),
	}
}

// Connect dials the GABP server and performs the handshake.
// Retries with exponential backoff until ctx is cancelled.
func (c *Client) Connect(ctx context.Context, addr string, token string, backoffMin, backoffMax time.Duration) error {
	c.token = token

	// Connect with retry/backoff
	var conn net.Conn
	var err error

	// Implement proper exponential backoff with jitter.
	// Respects backoffMin and backoffMax parameters with exponential growth
	// and randomized jitter to avoid thundering herd problems when multiple games
	// try to connect simultaneously.
	for attempts := 0; ; attempts++ {
		if ctx.Err() != nil {
			return fmt.Errorf("connect cancelled: %w", ctx.Err())
		}

		var d net.Dialer
		conn, err = d.DialContext(ctx, "tcp", addr)
		if err == nil {
			break
		}
		c.log.Warnw("connection attempt failed", "attempt", attempts+1, "error", err)

		if ctx.Err() != nil {
			return fmt.Errorf("connect cancelled after %d attempts: %w", attempts+1, ctx.Err())
		}

		// Calculate exponential backoff: backoffMin * 2^attempts
		multiplier := math.Pow(2, float64(attempts))
		backoffDelay := time.Duration(float64(backoffMin) * multiplier)
		// Cap at backoffMax
		if backoffDelay > backoffMax {
			backoffDelay = backoffMax
		}

		// Add jitter: ±25% randomization to prevent thundering herd
		jitterRange := float64(backoffDelay) * 0.25
		jitter := time.Duration(rand.Float64()*2*jitterRange - jitterRange)
		finalDelay := backoffDelay + jitter
		// Ensure we never go below backoffMin or above backoffMax
		if finalDelay < backoffMin {
			finalDelay = backoffMin
		}
		if finalDelay > backoffMax {
			finalDelay = backoffMax
		}

		c.log.Debugw("backing off before retry", "attempt", attempts+1, "delay", finalDelay, "baseDelay", backoffDelay)
		select {
		case <-time.After(finalDelay):
		case <-ctx.Done():
			return fmt.Errorf("connect cancelled during backoff: %w", ctx.Err())
		}
	}

	c.conn = conn
	c.writer = util.NewLSPFrameWriter(conn)
	c.reader = util.NewLSPFrameReader(conn)
	c.connected = true

	// Start the reader loop before the handshake so the welcome response can
	// be delivered to the pending request channel.
	go c.messageHandler()

	// Perform handshake in a way that observes ctx cancellation.
	handshakeErrCh := make(chan error, 1)
	go func() {
		handshakeErrCh <- c.handshake()
	}()

	select {
	case <-ctx.Done():
		// Context was cancelled while waiting for handshake; close the
		// connection to abort any in-flight operations and return the error.
		c.connected = false
		_ = conn.Close()
		return ctx.Err()
	case err := <-handshakeErrCh:
		if err != nil {
			// Handshake failed; ensure we do not leave the client in a
			// connected state and that the connection is cleaned up.
			c.connected = false
			_ = conn.Close()
			return err
		}
	}

	return nil
}

func (c *Client) handshake() error {
	// Send session/hello
	launchId := uuid.New().String()
	params := SessionHelloParams{
		Token:         c.token,
		BridgeVersion: version.Get(),  // Use actual runtime version
		Platform:      goruntime.GOOS, // Detect actual platform
		LaunchID:      launchId,
		ClientInfo: &ClientInfo{
			Name:    "gabs",
			Version: version.Get(),
		},
	}

	result, err := c.sendRequest(gabpruntime.MethodSessionHello, params)
	if err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

	// Parse welcome response
	var welcome SessionWelcomeResult
	if err := mapToStruct(result, &welcome); err != nil {
		return fmt.Errorf("failed to parse welcome: %w", err)
	}

	c.agentId = welcome.AgentID
	c.capabilities = welcome.Capabilities

	c.log.Infow("GABP handshake complete", "agentId", c.agentId, "methods", len(c.capabilities.Methods))
	return nil
}

func (c *Client) messageHandler() {
	var loopErr error
	defer c.markDisconnected(loopErr, true)

	for c.IsConnected() {
		data, err := c.reader.ReadMessage()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
				c.log.Errorw("failed to read message", "error", err)
			} else {
				c.log.Infow("GABP connection closed", "error", err)
			}
			loopErr = fmt.Errorf("failed to read message: %w", err)
			break
		}

		var msg util.GABPMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.log.Errorw("failed to unmarshal message", "error", err)
			continue
		}

		c.handleMessage(&msg)
	}
}

func (c *Client) handleMessage(msg *util.GABPMessage) {
	switch msg.Type {
	case gabpruntime.MessageTypeResponse:
		c.handleResponse(msg)
	case gabpruntime.MessageTypeEvent:
		c.handleEvent(msg)
	default:
		c.log.Warnw("unknown message type", "type", msg.Type, "id", msg.ID)
	}
}

func (c *Client) handleResponse(msg *util.GABPMessage) {
	c.mu.RLock()
	ch, exists := c.pendingReqs[msg.ID]
	c.mu.RUnlock()

	if exists {
		select {
		case ch <- msg:
		case <-time.After(5 * time.Second):
			c.log.Warnw("response channel timeout", "id", msg.ID)
		}
	} else {
		c.log.Warnw("received response for unknown request", "id", msg.ID)
	}
}

func (c *Client) handleEvent(msg *util.GABPMessage) {
	c.mu.RLock()
	handlers := c.eventHandlers[msg.Channel]
	c.mu.RUnlock()

	for _, handler := range handlers {
		go handler(msg.Channel, msg.Seq, msg.Payload)
	}
}

func (c *Client) sendRequest(method string, params interface{}) (interface{}, error) {
	return c.sendRequestWithTimeout(method, params, 30*time.Second)
}

func (c *Client) sendRequestWithTimeout(method string, params interface{}, timeout time.Duration) (interface{}, error) {
	req := util.NewGABPRequest(method, params)
	writer, disconnected, err := c.prepareRequest()
	if err != nil {
		return nil, err
	}

	// Register response channel
	respCh := make(chan *util.GABPMessage, 1)
	c.mu.Lock()
	c.pendingReqs[req.ID] = respCh
	c.mu.Unlock()

	// Clean up on exit
	defer func() {
		c.mu.Lock()
		delete(c.pendingReqs, req.ID)
		c.mu.Unlock()
	}()

	// Send request
	if err := writer.WriteJSON(req); err != nil {
		c.markDisconnected(fmt.Errorf("failed to write request: %w", err), true)
		return nil, c.connectionUnavailableError()
	}

	// Wait for response
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("GABP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-disconnected:
		return nil, c.connectionUnavailableError()
	case <-timer.C:
		return nil, fmt.Errorf("request timeout after %s", timeout)
	}
}

// ToolParameter represents a tool parameter from Lib.GAB
type ToolParameter struct {
	Name         string      `json:"name"`
	Type         string      `json:"type"`
	Description  string      `json:"description,omitempty"`
	Required     bool        `json:"required"`
	DefaultValue interface{} `json:"defaultValue,omitempty"`
}

// ToolDescriptorRaw is the raw format from Lib.GAB
type ToolDescriptorRaw struct {
	Name         string                 `json:"name"`
	Title        string                 `json:"title,omitempty"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  map[string]interface{} `json:"inputSchema,omitempty"`
	Parameters   []ToolParameter        `json:"parameters,omitempty"`
	OutputSchema map[string]interface{} `json:"outputSchema,omitempty"`
	RequiresAuth bool                   `json:"requiresAuth,omitempty"`
}

// ToolDescriptor is the normalized format for MCP
type ToolDescriptor struct {
	Name         string                 `json:"name"`
	Title        string                 `json:"title,omitempty"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  map[string]interface{} `json:"inputSchema,omitempty"`
	OutputSchema map[string]interface{} `json:"outputSchema,omitempty"`
	Tags         []string               `json:"tags,omitempty"`
}

// convertToToolDescriptor converts a raw Lib.GAB tool descriptor to MCP format
func convertToToolDescriptor(raw ToolDescriptorRaw) ToolDescriptor {
	inputSchema := raw.InputSchema
	if len(inputSchema) == 0 {
		inputSchema = buildInputSchemaFromParameters(raw.Parameters)
	}

	properties := make(map[string]interface{})
	required := []string{}
	_ = properties
	_ = required

	return ToolDescriptor{
		Name:         raw.Name,
		Title:        raw.Title,
		Description:  raw.Description,
		InputSchema:  inputSchema,
		OutputSchema: raw.OutputSchema,
	}
}

func buildInputSchemaFromParameters(parameters []ToolParameter) map[string]interface{} {
	properties := make(map[string]interface{})
	required := []string{}

	for _, p := range parameters {
		prop := map[string]interface{}{
			"type": mapTypeToJSONSchema(p.Type),
		}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if p.DefaultValue != nil {
			prop["default"] = p.DefaultValue
		}
		properties[p.Name] = prop

		if p.Required {
			required = append(required, p.Name)
		}
	}

	inputSchema := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		inputSchema["required"] = required
	}

	return inputSchema
}

// mapTypeToJSONSchema converts C# type names to JSON Schema types
func mapTypeToJSONSchema(typeName string) string {
	switch typeName {
	case "String", "string":
		return "string"
	case "Int32", "Int64", "int", "long":
		return "integer"
	case "Single", "Double", "float", "double":
		return "number"
	case "Boolean", "bool":
		return "boolean"
	default:
		return "string"
	}
}

func (c *Client) ListTools() ([]ToolDescriptor, error) {
	result, err := c.sendRequest(gabpruntime.MethodToolsList, map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	// The response is { "tools": [...] }, so extract the tools array
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response type: %T", result)
	}

	toolsData, exists := resultMap["tools"]
	if !exists {
		return []ToolDescriptor{}, nil
	}

	// Parse as raw format from Lib.GAB
	var rawTools []ToolDescriptorRaw
	if err := mapToStruct(toolsData, &rawTools); err != nil {
		return nil, fmt.Errorf("failed to parse tools: %w", err)
	}

	// Convert to MCP format
	tools := make([]ToolDescriptor, len(rawTools))
	for i, raw := range rawTools {
		tools[i] = convertToToolDescriptor(raw)
	}

	return tools, nil
}

func (c *Client) CallTool(name string, args map[string]any) (map[string]any, bool, error) {
	return c.CallToolWithTimeout(name, args, 30*time.Second)
}

// CallToolWithTimeout calls a tool with a custom timeout
func (c *Client) CallToolWithTimeout(name string, args map[string]any, timeout time.Duration) (map[string]any, bool, error) {
	params := map[string]interface{}{
		"name":       name,
		"parameters": args,
	}

	result, err := c.sendRequestWithTimeout(gabpruntime.MethodToolsCall, params, timeout)
	if err != nil {
		return nil, true, err
	}

	// Convert result to map
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return map[string]any{"value": result}, false, nil
	}

	return resultMap, false, nil
}

// SubscribeEvents subscribes to event channels
func (c *Client) SubscribeEvents(channels []string, handler EventHandler) error {
	// Register handler
	c.mu.Lock()
	for _, ch := range channels {
		c.eventHandlers[ch] = append(c.eventHandlers[ch], handler)
	}
	c.mu.Unlock()

	// Send subscription request
	params := map[string]interface{}{
		"channels": channels,
	}
	_, err := c.sendRequest("events/subscribe", params)
	return err
}

// GetCapabilities returns the server capabilities from the welcome response
func (c *Client) GetCapabilities() Capabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capabilities
}

// IsConnected reports whether the underlying GABP transport is still active.
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// DisconnectError returns the reason the GABP transport last disconnected.
func (c *Client) DisconnectError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.disconnectErr
}

// SetDisconnectHandler registers a callback invoked when the transport drops unexpectedly.
func (c *Client) SetDisconnectHandler(handler func(error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onDisconnect = handler
}

// Close gracefully closes the GABP connection
func (c *Client) Close() error {
	return c.markDisconnected(nil, false)
}

// mapToStruct converts a generic interface{} to a specific struct
func mapToStruct(src interface{}, dst interface{}) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func (c *Client) prepareRequest() (*util.LSPFrameWriter, <-chan struct{}, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.connected || c.writer == nil {
		return nil, nil, c.connectionUnavailableErrorLocked()
	}

	return c.writer, c.disconnected, nil
}

func (c *Client) connectionUnavailableError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connectionUnavailableErrorLocked()
}

func (c *Client) connectionUnavailableErrorLocked() error {
	if c.disconnectErr != nil {
		return fmt.Errorf("GABP connection unavailable: %w", c.disconnectErr)
	}
	return ErrClientNotConnected
}

func (c *Client) markDisconnected(err error, notify bool) error {
	var closeErr error
	var callback func(error)
	disconnectErr := err
	if disconnectErr == nil {
		disconnectErr = ErrClientClosed
	}

	c.disconnectOnce.Do(func() {
		c.mu.Lock()
		c.connected = false
		c.disconnectErr = disconnectErr
		callback = c.onDisconnect
		conn := c.conn
		c.mu.Unlock()

		if conn != nil {
			closeErr = conn.Close()
		}
		close(c.disconnected)
	})

	if notify && callback != nil {
		callback(disconnectErr)
	}

	return closeErr
}
