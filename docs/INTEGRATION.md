# AI Integration Guide

This guide shows you how to connect GABS to different AI assistants and tools.

> **See also:** [Configuration Guide](CONFIGURATION.md) for setting up games, [OpenAI Tool Normalization](OPENAI_TOOL_NORMALIZATION.md) for OpenAI API compatibility, and [Deployment Guide](DEPLOYMENT.md) for production setups.
>
> If you are starting from a downloaded release archive, read [AI Client Setup Guide](AI_CLIENT_SETUP.md) first. It covers unzip/install steps, config locations, and ready-to-paste client snippets.

## MCP Integration

GABS works as an MCP (Model Context Protocol) server. This means AI assistants can control your games through standard MCP tools.

### Available MCP Tools

Once GABS is running, AI can use these tools. Strict-safe names are advertised
by default; older dotted names remain accepted as call aliases.

- **`games_list`** - Show configured game IDs
- **`games_show`** - Show configuration and validation details for one game
- **`games_start`** - Start a game: `{"gameId": "factory"}`
- **`games_stop`** - Stop a game gracefully: `{"gameId": "factory"}`
- **`games_kill`** - Force quit a game: `{"gameId": "factory"}`
- **`games_status`** - Check if games are running: `{"gameId": "factory"}` or all games
- **`games_tool_names`** - Discover compact mirrored tool names
- **`games_tool_detail`** - Inspect one mirrored tool's schema
- **`games_tools`** - Fetch the richer compatibility listing of mirrored tools
- **`games_connect`** - Attach to a running game's GABP server after the bridge loads or after a GABS restart
- **`games_get_attention`** - Inspect a game's current blocking attention item
- **`games_ack_attention`** - Acknowledge the current blocking attention item and resume normal calls
- **`games_call_tool`** - Call a mirrored game tool through the stable core surface

Mirrored game tools are intentionally not advertised in the public `tools/list`
response. Discover them with `games_tool_names`, inspect one with
`games_tool_detail`, and call it through `games_call_tool`.

**Pro tip**: You can use either the game ID (`"adventure"`) or the launch target (`"123456"` for Steam) in any tool.

## Ownership and Reconnect Behavior

GABS coordinates live sessions per game with a short active-owner lease. If one
GABS session is actively using a running or starting game:

- `games_start` returns quickly instead of launching a duplicate copy
- `games_connect` returns quickly with an active-owner result instead of waiting
  on a competing bridge connection
- game-bound tool calls are blocked before they touch the bridge
- `games_status` may report that another GABS session owns the process and show
  the lease expiry
- `games_status` also reports runtime ownership and process-environment
  diagnostics without making `bridge.json` a recovery target
- if `games_start` reports `endpoint_cache_in_use`, use `games_connect` to
  attach to the already-listening endpoint or `resetEndpoint: true` only after
  confirming the cached endpoint should be rotated

Once the previous session is idle, `games_connect` naturally moves ownership to
the current session. If you intentionally want the current GABS session to take
ownership before the active lease expires, call:

```json
{
  "gameId": "adventure",
  "forceTakeover": true
}
```

with `games_connect`. This defaults to `false`.

## Attention-Aware Bridges

GABS is compatible with the additive attention surface introduced in GABP
v1.1 while staying on wire major `gabp/1`.

When a connected bridge advertises `attention/current` and `attention/ack`,
GABS can gate normal game-bound tool calls until the current attention item is
reviewed and acknowledged. The recovery flow is:

1. Call `games_get_attention`
2. Inspect the returned diagnostics or follow-up tooling
3. Call `games_ack_attention` with the returned `attentionId`
4. Retry the original game call

GABS still allows diagnostic and lifecycle observation tools through the gate so
an agent can understand the failure without disturbing the game further. Bridge
tools can opt into that behavior with generic `tools/list` tags such as
`diagnostic`, `health`, `lifecycle`, `observation`, `read-only`, `status`,
`telemetry`, or `attention-bypass`. Mutating gameplay and steering calls remain
blocked until attention is acknowledged.

## Setting Up AI Assistants

### OpenAI API Integration

Strict-safe tool name normalization is enabled by default when
`toolNormalization` is omitted. Keep it enabled for OpenAI and Claude variants:

```json
{
  "toolNormalization": {
    "enableOpenAINormalization": true,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  }
}
```

This converts tool names like `factory.inventory.get` to
`factory_inventory_get` for client compatibility. See
[Tool Normalization Guide](OPENAI_TOOL_NORMALIZATION.md) for complete details.

Some clients also reject `outputSchema` fields in `tools/list`. If Claude Code
or another MCP client disconnects with an `outputSchema.type` validation error,
add this to `~/.gabs/config.json`:

```json
{
  "stripOutputSchema": true
}
```

### Claude Desktop

Add this to your Claude Desktop MCP settings:

```json
{
  "mcpServers": {
    "gabs": {
      "command": "/path/to/gabs",
      "args": ["server"]
    }
  }
}
```

Then you can ask Claude:
- "List all my configured games"
- "Start factory and check its status"
- "Stop all running games"

### Codex CLI

Add this to your configuration:

```toml
[mcp_servers.gabs]
command = "/path/to/gabs"
args = ["server"]
```

Each live Codex session runs its own stdio GABS process. Cross-session
coordination happens only when those sessions interact with the same configured
game.

### Custom AI Tools

Here's a Python example using an MCP client:

```python
import mcp_client

# Connect to GABS
client = mcp_client.connect_stdio(["/path/to/gabs", "server"])

# List all games
games = client.call_tool("games_list", {})
print("Available games:", games)

# Start a specific game
result = client.call_tool("games_start", {"gameId": "factory"})
print("Start result:", result)

# Check status
status = client.call_tool("games_status", {"gameId": "factory"})
print("Game status:", status)
```

## Deployment Scenarios

### Local Development Setup
Perfect when your AI and games run on the same computer:

```bash
# 1. Configure your games
gabs games add factory
gabs games add adventure

# 2. Start GABS MCP server
gabs server

# 3. Configure your AI to connect (see examples above)
# 4. Ask AI to control your games!
```

### Remote AI Access to Local GABS
For AI tooling that reaches GABS over HTTP while the games still run on your
local machine:

**On the machine running GABS and the games:**
```bash
# 1. Add games normally
gabs games add factory

# 2. Start GABS in HTTP mode
gabs server --http :8080
```

GABP itself remains local-only. Your game-side bridge still listens on
`127.0.0.1:GABP_SERVER_PORT`; only the MCP HTTP surface is exposed remotely.

**Configure your remote AI client:**
```json
{
  "mcpServers": {
    "remote-gabs": {
      "command": "curl",
      "args": ["-X", "POST", "http://your-computer-ip:8080/mcp", 
               "-H", "Content-Type: application/json",
               "-d", "@-"]
    }
  }
}
```

Use firewall rules, reverse proxy authentication, or a VPN before exposing the
HTTP endpoint outside your machine or LAN.

### Game Server Farm Management
Let AI manage multiple game servers:

```bash
# Configure multiple servers
gabs games add factory-survival
gabs games add factory-creative
gabs games add adventure-a
gabs games add adventure-b

# Start GABS
gabs server

# AI can now control all servers through MCP tools
```

## HTTP Mode for Web Integration

GABS can also run as an HTTP server for web-based AI tools:

```bash
# Start HTTP mode
gabs server --http localhost:8080
```

Then use standard HTTP requests:
```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0", 
    "id": 1, 
    "method": "tools/call", 
    "params": {
      "name": "games_list",
      "arguments": {}
    }
  }'
```

## Example AI Conversations

Here are some examples of what you can ask your AI once GABS is set up:

**Starting games:**
- "Start my FactorySim server"
- "Launch AdventureGame and check if it started correctly"
- "Start all my configured games"

**Managing games:**
- "Stop the FactorySim server gracefully"
- "Kill any frozen games"
- "Show me the status of all my games"

**Advanced usage:**
- "Start the survival server, wait 30 seconds, then check if players can connect"
- "Restart the creative server if it's using too much memory"
- "Start a backup world while keeping the main server running"

## Troubleshooting Integration

### "AI can't see GABS tools"
1. Make sure GABS server is running: `gabs server`
2. Check your AI's MCP configuration file
3. Restart your AI assistant after changing configuration

### "Connection refused"
1. Verify GABS is running on the expected port
2. Check firewall settings for HTTP mode
3. Make sure the path to GABS binary is correct

### "Game won't start from AI"
1. Test the game manually first: `gabs games start factory`
2. Check that your game configuration is working: `gabs games show factory`
3. Make sure your game-side bridge supports GABP

### "HTTP mode not working"
1. Check if the port is already in use
2. Try a different port: `gabs server --http :8081`
3. Verify your HTTP client is sending proper JSON-RPC requests
