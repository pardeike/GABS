# AI Integration Guide

This guide shows you how to connect GABS to different AI assistants and tools.

## MCP Integration

GABS works as an MCP (Model Context Protocol) server. This means AI assistants can control your games through standard MCP tools.

### Available MCP Tools

Once GABS is running, AI can use these tools:

- **`games.list`** - Show all configured games and their status
- **`games.start`** - Start a game: `{"gameId": "minecraft"}`
- **`games.stop`** - Stop a game gracefully: `{"gameId": "minecraft"}`  
- **`games.kill`** - Force quit a game: `{"gameId": "minecraft"}`
- **`games.status`** - Check if games are running: `{"gameId": "minecraft"}` or all games

**Pro tip**: You can use either the game ID (`"rimworld"`) or the launch target (`"294100"` for Steam) in any tool.

## Setting Up AI Assistants

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

### Codeium/Codex CLI

Add this to your configuration:

```toml
[mcp_servers.gabs]
command = "/path/to/gabs"
args = ["server"]
```

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

### Remote Game Server Setup
For AI running in the cloud connecting to games on your computer:

**On your game computer:**
```bash
# 1. Add games with remote GABP mode
gabs games add minecraft
# When asked, choose "remote" GABP mode and enter your computer's IP

# 2. Start GABS in HTTP mode
gabs server --http :8080
```

**Configure your cloud AI:**
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