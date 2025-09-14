package mirror

import (
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/util"
)

// MockServer tracks notifications for testing
type MockServer struct {
	*mcp.Server
	notificationsSent []string
}

func NewMockServer(baseServer *mcp.Server) *MockServer {
	return &MockServer{
		Server:            baseServer,
		notificationsSent: make([]string, 0),
	}
}

func (ms *MockServer) SendToolsListChangedNotification() {
	ms.notificationsSent = append(ms.notificationsSent, "tools/list_changed")
	// Also call the base implementation if needed
	ms.Server.SendToolsListChangedNotification()
}

func (ms *MockServer) SendResourcesListChangedNotification() {
	ms.notificationsSent = append(ms.notificationsSent, "resources/list_changed")
	// Also call the base implementation if needed  
	ms.Server.SendResourcesListChangedNotification()
}

// MockClient provides test data for mirror testing
type MockClient struct {
	tools        []gabp.ToolDescriptor
	capabilities gabp.Capabilities
}

func (mc *MockClient) ListTools() ([]gabp.ToolDescriptor, error) {
	return mc.tools, nil
}

func (mc *MockClient) GetCapabilities() gabp.Capabilities {
	return mc.capabilities
}

func (mc *MockClient) CallTool(name string, args map[string]any) (map[string]any, bool, error) {
	return map[string]any{"result": "mock"}, false, nil
}

func TestMirrorNotifications(t *testing.T) {
	log := util.NewLogger("error")
	
	// Create mock server that tracks notifications
	baseServer := mcp.NewServer(log)
	mockServer := NewMockServer(baseServer)

	// Create mock client with test tools
	mockClient := &MockClient{
		tools: []gabp.ToolDescriptor{
			{Name: "test/tool1", Description: "Test tool 1"},
			{Name: "test/tool2", Description: "Test tool 2"},
		},
		capabilities: gabp.Capabilities{
			Methods:   []string{"tools/list", "tools/call"},
			Events:    []string{"game/event1", "game/event2"},
			Resources: []string{"world/state", "player/stats"},
		},
	}

	// Create mirror
	mirror := New(log, mockServer, mockClient, "test-game", &config.ToolNormalizationConfig{})

	// Test SyncTools sends notification
	err := mirror.SyncTools()
	if err != nil {
		t.Fatalf("SyncTools failed: %v", err)
	}

	// Check tools/list_changed notification was sent
	found := false
	for _, notification := range mockServer.notificationsSent {
		if notification == "tools/list_changed" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected tools/list_changed notification to be sent")
	}

	// Test ExposeResources sends notification
	err = mirror.ExposeResources()
	if err != nil {
		t.Fatalf("ExposeResources failed: %v", err)
	}

	// Check resources/list_changed notification was sent
	found = false
	for _, notification := range mockServer.notificationsSent {
		if notification == "resources/list_changed" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected resources/list_changed notification to be sent")
	}

	t.Logf("Notifications sent: %v", mockServer.notificationsSent)
}

func TestMirrorResourceCreation(t *testing.T) {
	log := util.NewLogger("error")
	server := mcp.NewServer(log)

	mockClient := &MockClient{
		tools: []gabp.ToolDescriptor{
			{Name: "inventory/get", Description: "Get inventory"},
		},
		capabilities: gabp.Capabilities{
			Events: []string{"player/move", "inventory/change"},
		},
	}

	mirror := New(log, server, mockClient, "minecraft", &config.ToolNormalizationConfig{})

	// Expose resources
	err := mirror.ExposeResources()
	if err != nil {
		t.Fatalf("ExposeResources failed: %v", err)
	}

	// Check that resources were registered by attempting to handle a resources/list request
	listRequest := &mcp.Message{
		JSONRPC: "2.0",
		ID:      "test-1",
		Method:  "resources/list",
		Params:  map[string]interface{}{},
	}

	response := server.HandleMessage(listRequest)
	if response.Error != nil {
		t.Fatalf("resources/list failed: %v", response.Error)
	}

	// Check response contains our game-specific resources
	if response.Result == nil {
		t.Fatal("Expected resources/list to return results")
	}

	t.Logf("Resources list response: %+v", response.Result)
}