package mcp

import (
	"io"
)

// Server runs MCP over stdio.
type Server struct {
	// PROMPT: fields for tool registry, resource registry, logger.
}

func NewServer() *Server {
	// PROMPT: construct server
	return &Server{}
}

func (s *Server) Serve(r io.Reader, w io.Writer) error {
	// PROMPT: Implement newline-delimited JSON-RPC over stdio per MCP stdio transport.
	// - Read one JSON object per line from r.
	// - Write JSON objects with trailing '\n' to w.
	// - Support initialize → return capabilities {tools:{listChanged:true}, resources:{...}}.
	// - Route "tools/list", "tools/call", "resources/list", "resources/read".
	// - Emit notifications/requests as needed.
	// - Set scanner buffer to ≥10MB to avoid truncation.
	return nil
}

// PROMPT: Registry methods to register tools/resources dynamically at runtime.
