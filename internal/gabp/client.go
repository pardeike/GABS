package gabp

import (
	"net"
	"time"
)

// Client speaks GABP over TCP NDJSON.
type Client struct {
	// PROMPT: fields: conn, enc/dec, token, agent info, logger, event ring buffers.
}

func (c *Client) Connect(addr string, token string, backoffMin, backoffMax time.Duration) error {
	// PROMPT: Connect to 127.0.0.1:port, authenticate with session/hello, expect session/welcome.
	// - Envelope: {v:"gabp/1", id, type, method, params|result|error}
	// - Only local loopback allowed; include token in params.
	// Spec: session/hello, session/welcome, capabilities & schemaVersion. :contentReference[oaicite:3]{index=3}
	_, _ = net.Dial, backoffMin
	return nil
}

type ToolDescriptor struct {
	// PROMPT: name,title,description,inputSchema,outputSchema,tags per GABP schema.
}

func (c *Client) ListTools() ([]ToolDescriptor, error) {
	// PROMPT: Send tools/list, parse array of tool descriptors.
	return nil, nil
}

func (c *Client) CallTool(name string, args map[string]any) (map[string]any, bool, error) {
	// PROMPT: tools/call → returns value; map to MCP ToolResult {content, structuredContent, isError}.
	return nil, false, nil
}

// PROMPT: Events subscribe/unsubscribe → maintain per-channel ring buffers, seq cursoring.
// PROMPT: Resources list/read → proxy stable URIs, fetch snapshots on demand.
