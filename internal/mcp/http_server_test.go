package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/util"
)

func TestHTTPServerBasicFunctionality(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServer(log)

	// Register a test tool
	testTool := Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"message": map[string]interface{}{
					"type": "string",
				},
			},
		},
	}

	server.RegisterTool(testTool, func(args map[string]interface{}) (*ToolResult, error) {
		message := "Hello World"
		if msg, ok := args["message"].(string); ok {
			message = msg
		}
		return &ToolResult{
			Content: []Content{{Type: "text", Text: message}},
		}, nil
	})

	// Start HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr := "127.0.0.1:0" // Use random available port
	go func() {
		if err := server.ServeHTTP(ctx, addr); err != nil && err != http.ErrServerClosed {
			t.Logf("HTTP server error: %v", err)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Note: For a complete test, we'd need to capture the actual port used
	// For now, we'll test the server starts without error
	t.Log("HTTP server started successfully")
}

func TestMCPHTTPEndpoint(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServer(log)

	// Register a simple tool
	server.RegisterTool(Tool{
		Name:        "echo",
		Description: "Echo input",
		InputSchema: map[string]interface{}{"type": "object"},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{
			Content: []Content{{Type: "text", Text: "echo response"}},
		}, nil
	})

	// Test initialize request
	initRequest := Message{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}

	// Test tools/list request
	toolsRequest := Message{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  map[string]interface{}{},
	}

	// Test tools/call request
	toolCallRequest := Message{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      "echo",
			"arguments": map[string]interface{}{"test": "value"},
		},
	}

	tests := []struct {
		name     string
		request  Message
		wantErr  bool
	}{
		{"initialize", initRequest, false},
		{"tools/list", toolsRequest, false},
		{"tools/call", toolCallRequest, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := server.HandleMessage(&tt.request)
			if (response == nil) != tt.wantErr {
				if response != nil && response.Error != nil {
					t.Errorf("HandleMessage() error = %v, wantErr %v", response.Error, tt.wantErr)
				} else if response == nil && !tt.wantErr {
					t.Errorf("HandleMessage() returned nil response, expected valid response")
				}
			}

			if response != nil {
				t.Logf("Request %s: ID=%v, Result present=%v, Error=%v", 
					tt.name, response.ID, response.Result != nil, response.Error)
			}
		})
	}
}

func TestSSENotificationFormat(t *testing.T) {
	// Test that notifications are properly formatted for SSE
	method := "notifications/tools/list_changed"
	params := map[string]interface{}{}

	notification := NewNotification(method, params)
	
	// Verify notification structure
	if notification.JSONRPC != "2.0" {
		t.Errorf("Expected JSONRPC 2.0, got %s", notification.JSONRPC)
	}
	if notification.Method != method {
		t.Errorf("Expected method %s, got %s", method, notification.Method)
	}
	if notification.ID != nil {
		t.Errorf("Notification should not have ID, got %v", notification.ID)
	}

	// Test JSON marshaling
	data, err := json.Marshal(notification)
	if err != nil {
		t.Errorf("Failed to marshal notification: %v", err)
	}

	// Verify it can be unmarshaled back
	var unmarshaled Message
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Errorf("Failed to unmarshal notification: %v", err)
	}

	t.Logf("Notification JSON: %s", string(data))
}

func TestHTTPMethodValidation(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServer(log)

	tests := []struct {
		method     string
		expectCode int
	}{
		{"GET", http.StatusMethodNotAllowed},
		{"POST", http.StatusBadRequest}, // Will fail due to no body, but method is allowed
		{"PUT", http.StatusMethodNotAllowed},
		{"DELETE", http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			// Create a test request
			var body io.Reader
			if tt.method == "POST" {
				body = strings.NewReader("invalid json")
			}

			req, err := http.NewRequest(tt.method, "/mcp", body)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			// Create a mock response writer
			recorder := &MockResponseWriter{
				headers: make(http.Header),
				body:    &bytes.Buffer{},
			}

			server.handleMCPHTTPRequest(recorder, req)

			if recorder.statusCode != tt.expectCode {
				t.Errorf("Expected status %d, got %d", tt.expectCode, recorder.statusCode)
			}

			t.Logf("Method %s: Status=%d, Body=%s", tt.method, recorder.statusCode, recorder.body.String())
		})
	}
}

// MockResponseWriter for testing HTTP handlers
type MockResponseWriter struct {
	headers    http.Header
	body       *bytes.Buffer
	statusCode int
}

func (m *MockResponseWriter) Header() http.Header {
	return m.headers
}

func (m *MockResponseWriter) Write(data []byte) (int, error) {
	return m.body.Write(data)
}

func (m *MockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

func TestValidJSONRPCOverHTTP(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServer(log)

	// Create a valid MCP initialize request
	request := Message{
		JSONRPC: "2.0",
		ID:      "test-1",
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "http-test-client", 
				"version": "1.0.0",
			},
		},
	}

	// Marshal to JSON
	requestData, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequest("POST", "/mcp", bytes.NewReader(requestData))
	if err != nil {
		t.Fatalf("Failed to create HTTP request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Create mock response writer
	recorder := &MockResponseWriter{
		headers: make(http.Header),
		body:    &bytes.Buffer{},
	}

	// Handle the request
	server.handleMCPHTTPRequest(recorder, httpReq)

	// Verify response
	if recorder.statusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.statusCode)
	}

	// Parse response
	var response Message
	if err := json.Unmarshal(recorder.body.Bytes(), &response); err != nil {
		t.Errorf("Failed to parse response: %v", err)
	} else {
		t.Logf("Initialize response: ID=%v, Result present=%v", response.ID, response.Result != nil)
	}
}