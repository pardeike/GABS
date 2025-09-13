package mcp

// PROMPT: Define minimal MCP types needed by server:
// - Initialize handling (protocol version, capabilities)
// - Tool definition {name,title,description,inputSchema,outputSchema,annotations?}
// - Tool call request/response {content[], structuredContent?, isError?}
// - Resources: list/read types
// Follow MCP 2025-06-18 spec. Messages are JSON-RPC 2.0 objects per stdio transport.
// Messages are newline-delimited JSON on stdio per spec. :contentReference[oaicite:1]{index=1}
