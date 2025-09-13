# GABS - A GABP compliant MCP Server (Go)

- **Purpose**: Bridge GABP mods to MCP hosts.
- **Transports**: MCP stdio (default). Optional Streamable HTTP. Newline-delimited JSON per MCP stdio. :contentReference[oaicite:5]{index=5}
- **GABS**: Implements `session/hello`, `tools/list`, `tools/call`, events, resources as per GABP 1.0. :contentReference[oaicite:6]{index=6}

## Build

```bash
go build ./cmd/gabs
GOOS=darwin  GOARCH=arm64  CGO_ENABLED=0 go build -o dist/gabs-darwin-arm64 ./cmd/gabs
GOOS=linux   GOARCH=amd64  CGO_ENABLED=0 go build -o dist/gabs-linux-amd64  ./cmd/gabs
GOOS=windows GOARCH=amd64  CGO_ENABLED=0 go build -o dist/gabs-windows-amd64.exe ./cmd/gabs