# AI Integration Guide

This guide shows you how to connect GABS to different AI assistants and tools.

> **See also:** [Configuration Guide](CONFIGURATION.md) for setting up games, [OpenAI Tool Normalization](OPENAI_TOOL_NORMALIZATION.md) for OpenAI API compatibility, and [Deployment Guide](DEPLOYMENT.md) for production setups.
>
> If you are starting from a downloaded release archive, read [AI Client Setup Guide](AI_CLIENT_SETUP.md) first. It covers unzip/install steps, config locations, and ready-to-paste client snippets.

## MCP Integration

GABS works as an MCP (Model Context Protocol) server. This means AI assistants can control your games through standard MCP tools.

### Available MCP Tools

Once GABS is running, AI can use these tools:

- **`games.list`** - Show configured game IDs
- **`games.show`** - Show configuration and validation details for one game
- **`games.start`** - Start a game: `{"gameId": "minecraft"}`
- **`games.stop`** - Stop a game gracefully: `{"gameId": "minecraft"}`
- **`games.kill`** - Force quit a game: `{"gameId": "minecraft"}`
- **`games.status`** - Check if games are running: `{"gameId": "minecraft"}` or all games
- **`games.tool_names`** - Discover compact mirrored tool names
- **`games.tool_detail`** - Inspect one mirrored tool's schema
- **`games.tools`** - Fetch the richer compatibility listing of mirrored tools
- **`games.connect`** - Attach to a running game's GABP server after the mod loads or after a GABS restart
- **`games.call_tool`** - Call a mirrored game tool through the stable core surface

**Pro tip**: You can use either the game ID (`"rimworld"`) or the launch target (`"294100"` for Steam) in any tool.

## Ownership and Reconnect Behavior

GABS coordinates live sessions per game. If one GABS session already owns a
running or starting game:

- `games.start` returns quickly instead of launching a duplicate copy
- `games.connect` returns quickly instead of waiting on a competing bridge
  connection
- `games.status` may report that another GABS session owns the process

If you intentionally want the current GABS session to take ownership of a
running game, call:

```json
{
  "gameId": "rimworld",
  "forceTakeover": true
}
```

with `games.connect`. This defaults to `false`.

## Setting Up AI Assistants

### OpenAI API Integration

For OpenAI API compatibility, you may need to enable tool name normalization. Add this to your configuration:

```json
{
  "toolNormalization": {
    "enableOpenAINormalization": true,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  }
}
```

This converts tool names like `minecraft.inventory.get` to `minecraft_inventory_get` for OpenAI compatibility. See [OpenAI Tool Normalization Guide](OPENAI_TOOL_NORMALIZATION.md) for complete details.

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
- "Start minecraft and check its status"
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
games = client.call_tool("games.list", {})
print("Available games:", games)

# Start a specific game
result = client.call_tool("games.start", {"gameId": "minecraft"})
print("Start result:", result)

# Check status
status = client.call_tool("games.status", {"gameId": "minecraft"})
print("Game status:", status)
```

## Deployment Scenarios

### Local Development Setup
Perfect when your AI and games run on the same computer:

```bash
# 1. Configure your games
gabs games add minecraft
gabs games add rimworld

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
gabs games add minecraft

# 2. Start GABS in HTTP mode
gabs server --http :8080
```

GABP itself remains local-only. Your game mod still listens on
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
gabs games add minecraft-survival
gabs games add minecraft-creative
gabs games add rimworld-colony1
gabs games add rimworld-colony2

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
      "name": "games.list", 
      "arguments": {}
    }
  }'
```

## Example AI Conversations

Here are some examples of what you can ask your AI once GABS is set up:

**Starting games:**
- "Start my Minecraft server"
- "Launch RimWorld and check if it started correctly"
- "Start all my configured games"

**Managing games:**
- "Stop the Minecraft server gracefully"
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
1. Test the game manually first: `gabs games start minecraft`
2. Check that your game configuration is working: `gabs games show minecraft`
3. Make sure your game mod supports GABP

### "HTTP mode not working"
1. Check if the port is already in use
2. Try a different port: `gabs server --http :8081`
3. Verify your HTTP client is sending proper JSON-RPC requests
