package mcp

// PROMPT: Optional Streamable HTTP transport per spec.
// - Single endpoint supporting POST/GET with SSE.
// - Validate Origin, bind to 127.0.0.1 for local, require auth if exposed.
// - Reuse same handlers as stdio server.
// Reference MCP Streamable HTTP rules. :contentReference[oaicite:2]{index=2}
