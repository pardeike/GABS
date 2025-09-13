package gabp

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pardeike/gabs/internal/util"
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
}

// EventHandler is a function that handles events
type EventHandler func(channel string, seq int, payload interface{})

// Capabilities represents server capabilities from welcome response
type Capabilities struct {
	Methods   []string     `json:"methods"`
	Events    []string     `json:"events"`
	Resources []string     `json:"resources"`
	Limits    *Limits      `json:"limits,omitempty"`
}

// Limits represents server limits
type Limits struct {
	MaxMessageSize        int `json:"maxMessageSize"`
	MaxConcurrentRequests int `json:"maxConcurrentRequests"`
	RequestTimeout        int `json:"requestTimeout"`
}

// SessionHelloParams represents the parameters for session/hello
type SessionHelloParams struct {
	Token         string      `json:"token"`
	BridgeVersion string      `json:"bridgeVersion"`
	Platform      string      `json:"platform"`
	LaunchId      string      `json:"launchId"`
	ClientInfo    *ClientInfo `json:"clientInfo,omitempty"`
}

// ClientInfo represents client information
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SessionWelcomeResult represents the result of session/welcome
type SessionWelcomeResult struct {
	AgentId       string       `json:"agentId"`
	App           *AppInfo     `json:"app,omitempty"`
	Capabilities  Capabilities `json:"capabilities"`
	SchemaVersion string       `json:"schemaVersion"`
	ServerInfo    *ServerInfo  `json:"serverInfo,omitempty"`
}

// AppInfo represents application information
type AppInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerInfo represents server information
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Author  string `json:"author,omitempty"`
}

// NewClient creates a new GABP client
func NewClient(log util.Logger) *Client {
	return &Client{
		pendingReqs:   make(map[string]chan *util.GABPMessage),
		eventHandlers: make(map[string][]EventHandler),
		sequences:     make(map[string]int),
		log:           log,
	}
}

func (c *Client) Connect(addr string, token string, backoffMin, backoffMax time.Duration) error {
	c.token = token
	
	// Connect with retry/backoff
	var conn net.Conn
	var err error
	
	// For now, simple connection without full backoff implementation
	// TODO: Implement proper exponential backoff
	for attempts := 0; attempts < 5; attempts++ {
		conn, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		c.log.Warnw("connection attempt failed", "attempt", attempts+1, "error", err)
		time.Sleep(backoffMin)
	}
	
	if err != nil {
		return fmt.Errorf("failed to connect after retries: %w", err)
	}

	c.conn = conn
	c.writer = util.NewLSPFrameWriter(conn)
	c.reader = util.NewLSPFrameReader(conn)
	c.connected = true

	// Start message handling goroutine
	go c.messageHandler()

	// Perform handshake
	return c.handshake()
}

func (c *Client) handshake() error {
	// Send session/hello
	launchId := uuid.New().String()
	params := SessionHelloParams{
		Token:         c.token,
		BridgeVersion: "1.0.0",
		Platform:      "linux", // TODO: detect actual platform
		LaunchId:      launchId,
		ClientInfo: &ClientInfo{
			Name:    "gabs",
			Version: "0.1.0",
		},
	}

	result, err := c.sendRequest("session/hello", params)
	if err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

	// Parse welcome response
	var welcome SessionWelcomeResult
	if err := mapToStruct(result, &welcome); err != nil {
		return fmt.Errorf("failed to parse welcome: %w", err)
	}

	c.agentId = welcome.AgentId
	c.capabilities = welcome.Capabilities

	c.log.Infow("GABP handshake complete", "agentId", c.agentId, "methods", len(c.capabilities.Methods))
	return nil
}

func (c *Client) messageHandler() {
	defer func() {
		c.connected = false
		if c.conn != nil {
			c.conn.Close()
		}
	}()

	for c.connected {
		data, err := c.reader.ReadMessage()
		if err != nil {
			c.log.Errorw("failed to read message", "error", err)
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
	case "response":
		c.handleResponse(msg)
	case "event":
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
	req := util.NewGABPRequest(method, params)
	
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
	if err := c.writer.WriteJSON(req); err != nil {
		return nil, fmt.Errorf("failed to write request: %w", err)
	}

	// Wait for response
	select {
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("GABP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("request timeout")
	}
}

type ToolDescriptor struct {
	Name         string                 `json:"name"`
	Title        string                 `json:"title,omitempty"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  map[string]interface{} `json:"inputSchema,omitempty"`
	OutputSchema map[string]interface{} `json:"outputSchema,omitempty"`
	Tags         []string               `json:"tags,omitempty"`
}

func (c *Client) ListTools() ([]ToolDescriptor, error) {
	result, err := c.sendRequest("tools/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	var tools []ToolDescriptor
	if err := mapToStruct(result, &tools); err != nil {
		return nil, fmt.Errorf("failed to parse tools: %w", err)
	}

	return tools, nil
}

func (c *Client) CallTool(name string, args map[string]any) (map[string]any, bool, error) {
	params := map[string]interface{}{
		"name":       name,
		"parameters": args,
	}

	result, err := c.sendRequest("tools/call", params)
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

// mapToStruct converts a generic interface{} to a specific struct
func mapToStruct(src interface{}, dst interface{}) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}
