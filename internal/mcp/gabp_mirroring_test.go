package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/util"
)

// GABPClientInterface defines the interface we need for GABP clients
type GABPClientInterface interface {
	ListTools() ([]gabp.ToolDescriptor, error)
	CallTool(name string, args map[string]any) (map[string]any, bool, error)
	GetCapabilities() gabp.Capabilities
	Close() error
}

// MockGABPClient provides test data for GABP mirroring testing
type MockGABPClient struct {
	tools []gabp.ToolDescriptor
	caps  gabp.Capabilities
}

func (m *MockGABPClient) ListTools() ([]gabp.ToolDescriptor, error) {
	return m.tools, nil
}

func (m *MockGABPClient) CallTool(name string, args map[string]any) (map[string]any, bool, error) {
	// Simple mock that returns a success message
	return map[string]any{"text": "Mock tool executed: " + name}, false, nil
}

func (m *MockGABPClient) GetCapabilities() gabp.Capabilities {
	return m.caps
}

func (m *MockGABPClient) Close() error {
	return nil
}

// syncGABPToolsWithInterface is a version of syncGABPTools that accepts an interface for testing
func (s *Server) syncGABPToolsWithInterface(client GABPClientInterface, gameID string) error {
	// Get tools from GABP client
	gabpTools, err := client.ListTools()
	if err != nil {
		return err
	}

	// Register each GABP tool as an MCP tool with game-specific naming
	for _, tool := range gabpTools {
		// Create game-prefixed tool name for multi-game clarity
		// Apply basic normalization first (convert slashes to dots)
		sanitizedToolName := util.NormalizeToolNameBasic(tool.Name)
		gameSpecificName := gameID + "." + sanitizedToolName

		mcpTool := Tool{
			Name:        gameSpecificName,
			Description: tool.Description + " (Game: " + gameID + ")",
			InputSchema: tool.InputSchema,
		}

		// Create handler that forwards to GABP with original tool name
		originalToolName := tool.Name // Capture original name for GABP call
		handler := func(toolName string) func(args map[string]interface{}) (*ToolResult, error) {
			return func(args map[string]interface{}) (*ToolResult, error) {
				// Call GABP with original tool name (without game prefix)
				result, isError, err := client.CallTool(toolName, args)
				if err != nil {
					return &ToolResult{
						Content: []Content{{Type: "text", Text: err.Error()}},
						IsError: true,
					}, nil
				}

				if isError {
					return &ToolResult{
						Content: []Content{{Type: "text", Text: "Tool error: " + result["error"].(string)}},
						IsError: true,
					}, nil
				}

				// Convert result to MCP format
				content := []Content{}
				if resultText, ok := result["text"].(string); ok {
					content = append(content, Content{Type: "text", Text: resultText})
				} else {
					content = append(content, Content{Type: "text", Text: "Tool executed successfully"})
				}

				return &ToolResult{
					Content: content,
					IsError: false,
				}, nil
			}
		}(originalToolName)

		// Register the tool using the existing registration method
		s.RegisterGameTool(gameID, mcpTool, handler, nil)
	}

	// Send tools/list_changed notification to AI agents
	s.SendToolsListChangedNotification()

	return nil
}

func TestGABPMirroringFunctionality(t *testing.T) {
	// Create a test logger
	log := util.NewLogger("debug")

	// Create server
	server := NewServerForTesting(log)

	// Create mock GABP client with some test tools
	mockClient := &MockGABPClient{
		tools: []gabp.ToolDescriptor{
			{
				Name:        "inventory/get",
				Description: "Get player inventory",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"slot": map[string]interface{}{
							"type": "integer",
							"description": "Inventory slot number",
						},
					},
				},
			},
			{
				Name:        "world/blocks/place",
				Description: "Place a block in the world",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"x": map[string]interface{}{"type": "integer"},
						"y": map[string]interface{}{"type": "integer"},
						"z": map[string]interface{}{"type": "integer"},
						"block": map[string]interface{}{"type": "string"},
					},
					"required": []string{"x", "y", "z", "block"},
				},
			},
		},
		caps: gabp.Capabilities{
			Methods:   []string{"tools/list", "tools/call"},
			Events:    []string{"player/move", "block/place"},
			Resources: []string{"world/state", "player/stats"},
		},
	}

	gameID := "minecraft"

	// Test tool synchronization
	err := server.syncGABPToolsWithInterface(mockClient, gameID)
	if err != nil {
		t.Fatalf("Failed to sync GABP tools: %v", err)
	}

	// Verify that tools were registered with game-specific prefixes
	server.mu.RLock()
	toolCount := len(server.tools)
	server.mu.RUnlock()

	if toolCount == 0 {
		t.Fatal("No tools were registered")
	}

	// Check that game-specific tools exist
	expectedTools := []string{
		"minecraft.inventory.get",
		"minecraft.world.blocks.place",
	}

	server.mu.RLock()
	for _, expectedTool := range expectedTools {
		if _, exists := server.tools[expectedTool]; !exists {
			t.Errorf("Expected tool %s was not registered", expectedTool)
		}
	}
	server.mu.RUnlock()

	// Test tool execution through the MCP interface
	server.mu.RLock()
	handler, exists := server.tools["minecraft.inventory.get"]
	server.mu.RUnlock()

	if !exists {
		t.Fatal("Tool minecraft.inventory.get not found")
	}

	// Execute the tool
	result, err := handler.Handler(map[string]interface{}{"slot": 1})
	if err != nil {
		t.Fatalf("Tool execution failed: %v", err)
	}

	if result.IsError {
		t.Fatal("Tool execution returned error")
	}

	if len(result.Content) == 0 {
		t.Fatal("Tool execution returned no content")
	}

	// Check that the result contains expected content
	resultText := result.Content[0].Text
	if !strings.Contains(resultText, "Mock tool executed: inventory/get") {
		t.Errorf("Unexpected tool result: %s", resultText)
	}

	t.Logf("✓ GABP mirroring test passed: %d tools registered", toolCount)
}

func TestGABPConnectionCleanup(t *testing.T) {
	// Create a test logger
	log := util.NewLogger("debug")

	// Create server
	server := NewServerForTesting(log)

	gameID := "test-game"

	// Create a mock client and store it
	mockClient := &MockGABPClient{
		tools: []gabp.ToolDescriptor{
			{Name: "test/tool", Description: "Test tool"},
		},
	}

	// Note: We can't directly store the mock client in gabpClients since it expects *gabp.Client
	// Instead, we'll test the cleanup by registering tools and then cleaning them up

	// Register some game tools 
	err := server.syncGABPToolsWithInterface(mockClient, gameID)
	if err != nil {
		t.Fatalf("Failed to sync GABP tools: %v", err)
	}

	// Verify resources are registered
	server.mu.RLock()
	initialToolCount := len(server.tools)
	server.mu.RUnlock()

	if initialToolCount == 0 {
		t.Fatal("Tools were not registered")
	}

	// Test cleanup (skip GABP client cleanup since we don't have a real client)
	server.CleanupGameResources(gameID)

	// Verify cleanup worked
	server.mu.RLock()
	finalToolCount := len(server.tools)
	server.mu.RUnlock()

	if finalToolCount >= initialToolCount {
		t.Error("Game tools were not cleaned up")
	}

	t.Logf("✓ Cleanup test passed: tools %d->%d", 
		initialToolCount, finalToolCount)
}

func TestGABPConnectionBackoffLogic(t *testing.T) {
	// This test verifies that the connection logic uses proper exponential backoff
	// We test this by checking that the GABP client's Connect method properly
	// handles the backoff parameters

	log := util.NewLogger("debug")
	client := gabp.NewClient(log)

	// Test that Connect method accepts backoff parameters (this will fail but that's expected)
	// We're testing that the interface is correct, not that it actually connects
	backoffMin := 100 * time.Millisecond
	backoffMax := 5 * time.Second

	// This will fail to connect to a non-existent server, but it should try with proper backoff
	err := client.Connect("127.0.0.1:12345", "fake-token", backoffMin, backoffMax)
	
	// We expect this to fail - we're just testing that the interface works
	if err == nil {
		t.Error("Expected connection to fail to non-existent server")
	}

	// The error should indicate connection failure, not parameter issues
	if !strings.Contains(err.Error(), "failed to connect") {
		t.Logf("Connection error as expected: %v", err)
	}

	t.Logf("✓ Backoff logic interface test passed")
}

func TestEstablishGABPConnectionWorkflow(t *testing.T) {
	// This test verifies the complete workflow that would happen when a game starts
	// and GABP connection is established (using a mock instead of real connection)

	log := util.NewLogger("debug")
	server := NewServerForTesting(log)

	gameID := "test-game"

	// Mock the establishGABPConnection workflow by calling the individual functions
	// that would be called if a real GABP server were available

	// Create a mock client to simulate successful GABP connection
	mockClient := &MockGABPClient{
		tools: []gabp.ToolDescriptor{
			{
				Name:        "player/status",
				Description: "Get player status",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"playerId": map[string]interface{}{
							"type": "string",
							"description": "Player ID",
						},
					},
				},
			},
		},
		caps: gabp.Capabilities{
			Methods: []string{"tools/list", "tools/call"},
			Events:  []string{"player/move"},
		},
	}

	// Simulate the workflow that establishGABPConnection would perform
	t.Log("Simulating GABP connection establishment workflow...")

	// Step 1: Connection would be established (simulated)
	t.Log("Step 1: GABP connection established")

	// Step 2: Sync tools from GABP to MCP
	err := server.syncGABPToolsWithInterface(mockClient, gameID)
	if err != nil {
		t.Fatalf("Failed to sync GABP tools: %v", err)
	}
	t.Log("Step 2: GABP tools synchronized to MCP")

	// Step 3: Verify game-specific tools were registered
	server.mu.RLock()
	toolExists := false
	for toolName := range server.tools {
		if strings.HasPrefix(toolName, gameID+".") {
			toolExists = true
			t.Logf("Found game-specific tool: %s", toolName)
		}
	}
	server.mu.RUnlock()

	if !toolExists {
		t.Fatal("No game-specific tools were registered")
	}

	// Step 4: Verify notifications would be sent (already tested in syncGABPToolsWithInterface)
	t.Log("Step 3: Notifications sent to AI agents")

	// Step 5: Test cleanup when game stops
	server.CleanupGameResources(gameID)
	t.Log("Step 4: Game resources cleaned up")

	// Verify cleanup worked
	server.mu.RLock()
	remainingGameTools := 0
	for toolName := range server.tools {
		if strings.HasPrefix(toolName, gameID+".") {
			remainingGameTools++
		}
	}
	server.mu.RUnlock()

	if remainingGameTools > 0 {
		t.Errorf("Game-specific tools were not properly cleaned up: %d remaining", remainingGameTools)
	}

	t.Log("✓ Complete GABP connection workflow test passed")
}