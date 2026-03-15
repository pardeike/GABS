package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/pardeike/gabs/internal/util"
)

func TestServeUsesLSPFramingForLSPClients(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServerForTesting(log)

	request := Message{
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

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var stdout bytes.Buffer
	wire := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if err := server.Serve(bytes.NewBufferString(wire), &stdout); err != nil {
		t.Fatalf("serve: %v", err)
	}

	reader := util.NewLSPFrameReader(bytes.NewReader(stdout.Bytes()))
	responseData, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("read lsp response: %v", err)
	}

	var response Message
	if err := json.Unmarshal(responseData, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if response.ID != float64(1) && response.ID != 1 {
		t.Fatalf("unexpected response id: %#v", response.ID)
	}
	if response.Result == nil {
		t.Fatalf("expected initialize result, got nil")
	}
}

func TestServeKeepsNewlineCompatibility(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServerForTesting(log)

	request := Message{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "legacy-client",
				"version": "1.0.0",
			},
		},
	}

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var stdout bytes.Buffer
	if err := server.Serve(bytes.NewBuffer(append(body, '\n')), &stdout); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var response Message
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &response); err != nil {
		t.Fatalf("unmarshal newline response: %v", err)
	}

	if response.ID != float64(2) && response.ID != 2 {
		t.Fatalf("unexpected response id: %#v", response.ID)
	}
	if response.Result == nil {
		t.Fatalf("expected initialize result, got nil")
	}
}
