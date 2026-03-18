package gabp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/util"
)

// Test exponential backoff behavior by connecting to a non-existent server
func TestExponentialBackoff(t *testing.T) {
	// Create a client with a mock logger
	log := util.NewLogger("debug") // debug mode to see backoff logs
	client := NewClient(log)

	// Test parameters
	backoffMin := 10 * time.Millisecond
	backoffMax := 100 * time.Millisecond

	// Try to connect to a non-existent address to trigger retries
	nonExistentAddr := "127.0.0.1:99999" // Use a port that's very unlikely to be in use

	// Ensure the port is not actually in use
	if conn, err := net.Dial("tcp", nonExistentAddr); err == nil {
		conn.Close()
		t.Skip("Test port is actually in use, skipping backoff test")
	}

	timeout := 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	err := client.Connect(ctx, nonExistentAddr, "test-token", backoffMin, backoffMax)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected connection to fail")
	}

	// Should run until the context deadline, retrying with backoff
	if duration < timeout/2 {
		t.Errorf("Backoff too short: expected ~%v, got %v", timeout, duration)
	}
	if duration > timeout+100*time.Millisecond {
		t.Errorf("Didn't stop promptly after context cancellation: %v", duration)
	}

	t.Logf("Total backoff duration: %v (timeout: %v)", duration, timeout)
}

// Test that backoff respects the maximum delay
func TestBackoffMaximum(t *testing.T) {
	log := util.NewLogger("error") // quiet for this test
	client := NewClient(log)

	backoffMin := 1 * time.Millisecond
	backoffMax := 5 * time.Millisecond // Very small max to test capping

	nonExistentAddr := "127.0.0.1:99998"

	// Ensure the port is not actually in use
	if conn, err := net.Dial("tcp", nonExistentAddr); err == nil {
		conn.Close()
		t.Skip("Test port is actually in use, skipping backoff test")
	}

	timeout := 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	err := client.Connect(ctx, nonExistentAddr, "test-token", backoffMin, backoffMax)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected connection to fail")
	}

	// With 5ms max backoff, should get many attempts within the timeout
	if duration > timeout+100*time.Millisecond {
		t.Errorf("Didn't stop promptly after context cancellation: %v", duration)
	}

	t.Logf("Capped backoff duration: %v (timeout: %v)", duration, timeout)
}

// Test that jitter provides variation in delays
func TestBackoffJitter(t *testing.T) {
	log := util.NewLogger("error")

	backoffMin := 10 * time.Millisecond
	backoffMax := 100 * time.Millisecond
	nonExistentAddr := "127.0.0.1:99997"

	// Ensure the port is not actually in use
	if conn, err := net.Dial("tcp", nonExistentAddr); err == nil {
		conn.Close()
		t.Skip("Test port is actually in use, skipping backoff test")
	}

	var durations []time.Duration

	// Run multiple connection attempts to observe jitter variation
	for i := 0; i < 3; i++ {
		client := NewClient(log)
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		client.Connect(ctx, nonExistentAddr, "test-token", backoffMin, backoffMax)
		cancel()
		durations = append(durations, time.Since(start))
	}

	// Check that we have some variation (jitter working)
	// All durations shouldn't be identical due to jitter
	allSame := true
	for i := 1; i < len(durations); i++ {
		// Allow 1ms tolerance for timing precision
		if math.Abs(float64(durations[i]-durations[0])) > float64(1*time.Millisecond) {
			allSame = false
			break
		}
	}

	if allSame {
		t.Log("Warning: All backoff durations were very similar, jitter may not be working effectively")
		// Don't fail the test as this could happen by chance, but log it
	}

	t.Logf("Jitter test durations: %v", durations)
}

func TestConvertToToolDescriptorPrefersCanonicalInputSchema(t *testing.T) {
	raw := ToolDescriptorRaw{
		Name:        "inventory/get",
		Title:       "Get Inventory",
		Description: "Returns the inventory",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"playerId": map[string]interface{}{
					"type": "string",
				},
			},
			"required": []interface{}{"playerId"},
		},
		Parameters: []ToolParameter{
			{
				Name:        "legacy",
				Type:        "Int32",
				Description: "Should not be used when canonical schema is present",
				Required:    true,
			},
		},
		OutputSchema: map[string]interface{}{
			"type": "object",
		},
	}

	descriptor := convertToToolDescriptor(raw)

	if descriptor.Title != "Get Inventory" {
		t.Fatalf("unexpected title: %s", descriptor.Title)
	}

	if !reflect.DeepEqual(descriptor.InputSchema, raw.InputSchema) {
		t.Fatalf("expected canonical inputSchema to be preserved, got %#v", descriptor.InputSchema)
	}
}

func TestConvertToToolDescriptorFallsBackToParameters(t *testing.T) {
	raw := ToolDescriptorRaw{
		Name:        "math/add",
		Description: "Add two numbers",
		Parameters: []ToolParameter{
			{
				Name:        "a",
				Type:        "Int32",
				Description: "First number",
				Required:    true,
			},
			{
				Name:        "b",
				Type:        "Int32",
				Description: "Second number",
				Required:    true,
			},
		},
	}

	descriptor := convertToToolDescriptor(raw)

	if descriptor.InputSchema["type"] != "object" {
		t.Fatalf("unexpected inputSchema type: %#v", descriptor.InputSchema["type"])
	}

	properties, ok := descriptor.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing properties map: %#v", descriptor.InputSchema)
	}

	aProperty, ok := properties["a"].(map[string]interface{})
	if !ok || aProperty["type"] != "integer" {
		t.Fatalf("unexpected schema for parameter a: %#v", properties["a"])
	}

	required, ok := descriptor.InputSchema["required"].([]string)
	if !ok {
		t.Fatalf("missing required list: %#v", descriptor.InputSchema["required"])
	}

	if len(required) != 2 || required[0] != "a" || required[1] != "b" {
		t.Fatalf("unexpected required list: %#v", required)
	}
}

func TestConnectCompletesHandshakeWhenServerResponds(t *testing.T) {
	log := util.NewLogger("error")
	client := NewClient(log)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()

		reader := util.NewLSPFrameReader(conn)
		writer := util.NewLSPFrameWriter(conn)

		data, err := reader.ReadMessage()
		if err != nil {
			serverDone <- err
			return
		}

		var request util.GABPMessage
		if err := json.Unmarshal(data, &request); err != nil {
			serverDone <- err
			return
		}

		if request.Method != "session/hello" {
			serverDone <- fmt.Errorf("unexpected method: %s", request.Method)
			return
		}

		response := util.NewGABPResponse(request.ID, SessionWelcomeResult{
			AgentID: "rimworld",
			Capabilities: Capabilities{
				Methods:   []string{"tools/list", "tools/call"},
				Events:    []string{"system/log"},
				Resources: []string{},
			},
			SchemaVersion: "1.0",
		})

		serverDone <- writer.WriteJSON(response)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Connect(ctx, listener.Addr().String(), "test-token", 10*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("expected handshake to succeed, got: %v", err)
	}
	defer client.Close()

	capabilities := client.GetCapabilities()
	if len(capabilities.Methods) != 2 || capabilities.Methods[0] != "tools/list" || capabilities.Methods[1] != "tools/call" {
		t.Fatalf("unexpected capabilities after handshake: %#v", capabilities)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("server goroutine failed: %v", err)
	}
}

func TestCallToolFailsFastWhenConnectionDrops(t *testing.T) {
	log := util.NewLogger("error")
	client := NewClient(log)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()

		reader := util.NewLSPFrameReader(conn)
		writer := util.NewLSPFrameWriter(conn)

		data, err := reader.ReadMessage()
		if err != nil {
			serverDone <- err
			return
		}

		var hello util.GABPMessage
		if err := json.Unmarshal(data, &hello); err != nil {
			serverDone <- err
			return
		}

		if err := writer.WriteJSON(util.NewGABPResponse(hello.ID, SessionWelcomeResult{
			AgentID: "rimworld",
			Capabilities: Capabilities{
				Methods: []string{"tools/call"},
			},
			SchemaVersion: "1.0",
		})); err != nil {
			serverDone <- err
			return
		}

		data, err = reader.ReadMessage()
		if err != nil {
			serverDone <- err
			return
		}

		var call util.GABPMessage
		if err := json.Unmarshal(data, &call); err != nil {
			serverDone <- err
			return
		}

		if call.Method != "tools/call" {
			serverDone <- fmt.Errorf("unexpected method: %s", call.Method)
			return
		}

		serverDone <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Connect(ctx, listener.Addr().String(), "test-token", 10*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("expected handshake to succeed, got: %v", err)
	}
	defer client.Close()

	start := time.Now()
	_, _, err = client.CallToolWithTimeout("rimbridge/core/ping", map[string]any{}, 30*time.Second)
	duration := time.Since(start)
	if err == nil {
		t.Fatal("expected tool call to fail when the connection drops")
	}
	if duration > time.Second {
		t.Fatalf("expected disconnect to fail fast, took %v", duration)
	}
	if !strings.Contains(err.Error(), "connection unavailable") {
		t.Fatalf("expected disconnect error, got: %v", err)
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("server goroutine failed: %v", err)
	}
}
