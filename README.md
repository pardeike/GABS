# GABS - Game Agent Bridge Server

**Configuration-first MCP server for AI-powered game automation!**

GABS is a universal bridge that connects AI tools to GABP compliant modifications in your games. With its new configuration-first architecture, you define your games once and then control them naturally through AI using MCP tools.

## Why GABS?

**For Game Modders:**
- **AI-Powered Development**: Let AI assistants help debug, test, and develop your mods
- **Configuration-First**: Define games once, control via AI naturally
- **Universal Tool**: Works with any game, any mod framework, any AI assistant
- **Cross-Platform**: One binary runs on Windows, macOS, and Linux
- **Zero Setup**: Configure once and you're ready to go
- **Secure**: Local-only connections with token authentication

**Real Examples:**
- Ask AI "start minecraft and check if the server is running"
- Have AI automatically test your new crafting recipe across multiple games
- Let AI assistants monitor game status and restart crashed servers
- Debug multiplayer sync issues with AI controlling multiple game instances

## What is GABS?

GABS implements the [GABP (Game Agent Bridge Protocol)](https://github.com/pardeike/GABP) - a standard way for AI tools to communicate with games through GABP compliant mods. It serves as an MCP server that exposes game control capabilities to AI assistants.

**Key Architecture Insight:** GABS uses a **configuration-first approach**. You define all your games once, then AI controls them naturally through MCP tools instead of complex CLI commands.

```
Your AI Assistant ← MCP Tools → GABS ← GABP → Game Mod ← → Your Game
                    games.start           (mod=server)
                    games.stop
                    games.list
```

**Key Features:**
- **Configuration-first**: Define games once in config, control via AI
- **MCP-native**: Game management through natural MCP tool calls
- **Works with any game**: Not tied to specific games or engines  
- **Works with any AI**: Compatible with ChatGPT, Claude, local LLMs, and custom AI tools
- **Easy integration**: Simple JSON API that any mod can implement
- **Real-time events**: AI gets live updates as things happen in your game
- **Resource access**: AI can read game files, configs, and documentation

## Quick Start

### 1. Download GABS

Choose your platform:
- **Windows**: [`gabs-windows-amd64.exe`](../../releases/latest)
- **macOS**: [`gabs-darwin-arm64`](../../releases/latest) 
- **Linux**: [`gabs-linux-amd64`](../../releases/latest)

### 2. Configure Your Games

```bash
# Add your games to the configuration
gabs games add minecraft
gabs games add rimworld

# View configured games
gabs games list
```

### 3. Start the MCP Server

```bash
# For AI assistants (stdio)
gabs server

# For HTTP-based tools  
gabs server --http localhost:8080
```

### 4. AI Control via MCP Tools

Once the server is running, AI can use these MCP tools:

- **`games.list`** - List all configured games and their status
- **`games.start`** - Start a game: `{"gameId": "minecraft"}`
- **`games.stop`** - Stop a game gracefully: `{"gameId": "minecraft"}`
- **`games.kill`** - Force terminate: `{"gameId": "minecraft"}`
- **`games.status`** - Check status: `{"gameId": "minecraft"}` or all games

## Configuration Management

### Adding Games

```bash
# Interactive configuration
gabs games add minecraft

# View game details
gabs games show minecraft  

# Remove a game
gabs games remove minecraft
```

### Configuration File

Games are stored in platform-specific locations:

- **Windows**: `%APPDATA%/GABS/config.json`
- **macOS**: `~/Library/Application Support/GABS/config.json`  
- **Linux**: `~/.config/gabs/config.json`

Example configuration:
```json
{
  "version": "1.0",
  "games": {
    "minecraft": {
      "id": "minecraft",
      "name": "Minecraft Server",
      "launchMode": "DirectPath",
      "target": "/opt/minecraft/start.sh",
      "workingDir": "/opt/minecraft",
      "gabpMode": "local",
      "description": "Main Minecraft server"
    },
    "rimworld": {
      "id": "rimworld", 
      "name": "RimWorld",
      "launchMode": "SteamAppId",
      "target": "294100",
      "gabpMode": "local"
    }
  }
}
```

## AI Integration Examples

### With Claude Desktop (MCP)

Add to your MCP settings:
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

Then ask Claude:
- "List all my configured games"
- "Start minecraft and check its status"
- "Stop all running games"

### With Custom AI Tools

```python
# Python example using MCP client
import mcp_client

client = mcp_client.connect_stdio(["/path/to/gabs", "server"])

# List games
games = client.call_tool("games.list", {})
print(games)

# Start a game
result = client.call_tool("games.start", {"gameId": "minecraft"})
```

## Deployment Scenarios

### Local Development
Perfect for development where AI and games run on the same machine:
```bash
gabs games add mygame
gabs server
# AI connects and controls games locally
```

### Cloud AI + Remote Games
For AI running in cloud connecting to games on remote machines, configure games with remote GABP settings:

```json
{
  "id": "minecraft",
  "name": "Remote Minecraft",
  "gabpMode": "remote", 
  "gabpHost": "192.168.1.100"
}
```

### Game Server Management
Use GABS to let AI manage multiple game servers:
```bash
gabs games add minecraft-survival
gabs games add minecraft-creative  
gabs games add rimworld-colony1
gabs server --http :8080
```

AI can then manage all servers through a single interface.

## For Mod Developers

### Adding GABP Support to Your Mod

To work with GABS, your mod must implement the GABP protocol as a server:

1. **Read the bridge config** when GABS starts your game:
   ```json
   {
     "port": 12345,
     "token": "secret-auth-token", 
     "gameId": "your-game-id",
     "agentName": "gabs-v0.1.0",
     "host": "127.0.0.1",
     "mode": "local"
   }
   ```

2. **Act as GABP server** - listen on the specified port for GABS connections

3. **Expose your functionality** as tools, resources, and events per the GABP spec

### Example Integration

```csharp
// C# example for Unity/Harmony mods
public class GABPMod : Mod {
    void Start() {
        var config = ReadBridgeConfig();  // Read from bridge.json
        var server = new GABPServer(config.port, config.token);
        
        // Register your mod's capabilities
        server.RegisterTool("inventory/get", GetInventory);
        server.RegisterTool("world/place_block", PlaceBlock);
        server.RegisterEvent("player/move");
        
        server.Listen(); // Act as GABP server for GABS to connect to
    }
}
```

## Advanced Usage

### Multiple Game Instances

```bash
# Configure multiple instances of the same game
gabs games add minecraft-server1
gabs games add minecraft-server2

# AI can manage them separately
games.start {"gameId": "minecraft-server1"}
games.start {"gameId": "minecraft-server2"}
```

### Custom Launch Modes

GABS supports multiple ways to launch games:

- **DirectPath**: Direct executable path
- **SteamAppId**: Launch via Steam App ID  
- **EpicAppId**: Launch via Epic Games Store
- **CustomCommand**: Custom launch command with arguments

### HTTP Mode for Web Integration

```bash
# Run as HTTP server for web-based AI tools
gabs server --http localhost:8080

# Use standard HTTP MCP protocol
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "games.list", "arguments": {}}}'
```

## Build from Source

Requirements: Go 1.22+

```bash
# Build for your platform
go build ./cmd/gabs

# Build for all platforms
make build-all
```

## Contributing

We welcome contributions! Whether you're:
- A game modder wanting to add GABP support to your favorite game
- An AI developer building new automation tools  
- Someone improving documentation or examples

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

GABS is licensed under the MIT License. See [LICENSE](LICENSE) for details.

The GABP protocol specification is licensed under CC BY 4.0.

## FAQ

**Q: How is this different from the old CLI-heavy approach?**  
A: The new architecture separates concerns: CLI manages configuration, MCP tools manage game lifecycle. Instead of `gabs run --gameId minecraft --launch DirectPath --target /path`, you configure once with `gabs games add minecraft` and then AI uses `games.start {"gameId": "minecraft"}`.

**Q: Can I migrate from the old CLI commands?**  
A: Yes! The old bridge.json files are still supported. The new system reads them for backward compatibility while providing the cleaner configuration-first approach.

**Q: Can multiple AI tools control games simultaneously?**  
A: Currently, one AI tool per GABS instance. Run multiple GABS instances for multiple AI connections, or coordinate through the AI tools themselves.

**Q: Does this work with multiplayer games?**  
A: GABS connects to your local mod instance. Multiplayer compatibility depends on your mod's design.

**Q: Is this secure?**  
A: GABS only accepts local connections by default and uses token authentication. Your games never expose ports directly to the internet unless you explicitly configure remote access.

**Q: What games are supported?**  
A: Any game where you can add GABP compliant mods! Popular targets include Unity games, Java games (Minecraft), and games that support C#/Harmony modding.