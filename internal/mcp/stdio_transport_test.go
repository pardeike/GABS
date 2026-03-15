package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

func TestServeIgnoresInitializedNotification(t *testing.T) {
	log := util.NewLogger("error")
	server := NewServerForTesting(log)

	initialize := Message{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "codex-mcp-client",
				"version": "0.114.0",
			},
		},
	}
	initialized := Message{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
		Params:  map[string]interface{}{},
	}
	toolsList := Message{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
		Params:  map[string]interface{}{},
	}

	var stdin bytes.Buffer
	for _, msg := range []Message{initialize, initialized, toolsList} {
		body, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		fmt.Fprintf(&stdin, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}

	var stdout bytes.Buffer
	if err := server.Serve(&stdin, &stdout); err != nil {
		t.Fatalf("serve: %v", err)
	}

	reader := util.NewLSPFrameReader(bytes.NewReader(stdout.Bytes()))

	firstResponseData, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("read initialize response: %v", err)
	}

	var firstResponse Message
	if err := json.Unmarshal(firstResponseData, &firstResponse); err != nil {
		t.Fatalf("unmarshal initialize response: %v", err)
	}
	if firstResponse.ID != float64(1) && firstResponse.ID != 1 {
		t.Fatalf("unexpected initialize response id: %#v", firstResponse.ID)
	}
	if firstResponse.Result == nil {
		t.Fatal("expected initialize result, got nil")
	}

	secondResponseData, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("read tools/list response: %v", err)
	}

	var secondResponse Message
	if err := json.Unmarshal(secondResponseData, &secondResponse); err != nil {
		t.Fatalf("unmarshal tools/list response: %v", err)
	}
	if secondResponse.ID != float64(2) && secondResponse.ID != 2 {
		t.Fatalf("unexpected tools/list response id: %#v", secondResponse.ID)
	}
	if secondResponse.Error != nil {
		t.Fatalf("unexpected tools/list error: %+v", secondResponse.Error)
	}

	if _, err := reader.ReadMessage(); err != io.EOF {
		t.Fatalf("expected no response for initialized notification, got err=%v", err)
	}
}
