# GABS - Game Agent Bridge Server

**Make your game mods AI-ready in minutes!**

GABS is a universal bridge that connects AI tools to GABP compliant modifications in your games. Whether you're modding Minecraft, RimWorld, or any other game, GABS lets AI assistants understand and interact with your mods automatically by connecting to mods that implement the GABP (Game Agent Bridge Protocol).

## Why GABS?

**For Game Modders:**
- **AI-Powered Development**: Let AI assistants help debug, test, and develop your mods
- **Universal Tool**: Works with any game, any mod framework, any AI assistant
- **Cross-Platform**: One binary runs on Windows, macOS, and Linux
- **Zero Setup**: Drop in the binary and you're ready to go
- **Secure**: Local-only connections with token authentication

**Real Examples:**
- Ask AI to test your new crafting recipe while you code
- Have AI automatically detect bugs in your building system
- Let AI assistants read your mod's documentation and help users
- Debug multiplayer sync issues with AI monitoring game state

## What is GABS?

GABS implements the [GABP (Game Agent Bridge Protocol)](https://github.com/pardeike/GABP) - a standard way for AI tools to communicate with games through GABP compliant mods. Think of it as a translator that lets AI assistants "speak" to your game mods that have implemented the GABP protocol.

The server connects to mods in your game that support GABP. These can be:
- **Central community mods** that search for and expose tools from all installed mods
- **Individual mods** using a GABP framework to expose their own functionality  
- **General game control mods** that make the entire game remotely controllable (not just specific mod features)
- **Combined approaches** where you can control both the game itself and specific mod functionality for ultimate control

```
Your AI Assistant ← → GABS ← → GABP Compliant Mod ← → Your Game
```

**Key Features:**
- **Works with any game**: Not tied to specific games or engines  
- **Works with any AI**: Compatible with ChatGPT, Claude, local LLMs, and custom AI tools
- **Easy integration**: Simple JSON API that any mod can implement
- **Real-time events**: AI gets live updates as things happen in your game
- **Resource access**: AI can read game files, configs, and documentation
- **Tool execution**: AI can trigger actions in your game

## Quick Start

### 1. Download GABS

Choose your platform:
- **Windows**: [`gabs-windows-amd64.exe`](../../releases/latest)
- **macOS**: [`gabs-darwin-arm64`](../../releases/latest) 
- **Linux**: [`gabs-linux-amd64`](../../releases/latest)

### 2. Basic Usage

```bash
# Launch your game with GABS bridge
gabs run --gameId minecraft --launch DirectPath --target "/path/to/minecraft"

# Or connect to already running game
gabs attach --gameId minecraft

# Check if everything is working  
gabs status --gameId minecraft
```

### 3. Integration with AI Tools

GABS acts as an MCP (Model Context Protocol) server, so it works automatically with:
- **Claude Desktop** (with MCP support)
- **VS Code** (with MCP extensions)
- **Custom AI tools** (using MCP protocol)

## Supported Launch Modes

GABS can start your game in multiple ways:

```bash
# Direct executable path
gabs run --gameId mygame --launch DirectPath --target "/path/to/game.exe"

# Steam games (by App ID)
gabs run --gameId rimworld --launch SteamAppId --target 294100

# Epic Games
gabs run --gameId mygame --launch EpicAppId --target "GameIdentifier"

# Custom command with arguments
gabs run --gameId mygame --launch CustomCommand --target "launcher.exe" --arg "--windowed" --arg "--debug"
```

## Configuration

GABS automatically creates configuration files in platform-specific locations:

- **Windows**: `%APPDATA%/GAB/your-game-id/`
- **macOS**: `~/Library/Application Support/GAB/your-game-id/`  
- **Linux**: `~/.local/state/gab/your-game-id/`

The `bridge.json` file contains connection details that your mod reads to connect to GABS.

## For Mod Developers

### Adding GABP Support to Your Mod

To work with GABS, your mod must implement the GABP protocol. This makes your mod "GABP compliant" and allows GABS to connect to it.

1. **Read the bridge config** when your mod starts:
   ```json
   {
     "port": 12345,
     "token": "secret-auth-token",
     "launchId": "unique-session-id"
   }
   ```

2. **Connect to GABS** using the GABP protocol (see [GABP Specification](https://github.com/pardeike/GABP))

3. **Expose your functionality** as tools, resources, and events

### Example Integration

```csharp
// C# example for Unity/Harmony mods
public class GABPMod : Mod {
    void Start() {
        var config = ReadBridgeConfig();  // Read port/token from bridge.json
        var client = new GABPClient(config.port, config.token);
        
        // Register your mod's capabilities
        client.RegisterTool("inventory/get", GetInventory);
        client.RegisterTool("world/place_block", PlaceBlock);
        client.RegisterEvent("player/move");
        
        client.Connect();
    }
}
```

## Documentation

- **[AGENTS.md](AGENTS.md)** - Complete implementation guide for AI agents
- **[GABP Specification](https://github.com/pardeike/GABP)** - Protocol details and schemas
- **[Examples](https://github.com/pardeike/GABP/tree/main/EXAMPLES)** - Real message examples

## Advanced Usage

### HTTP Mode (for web-based AI tools)

```bash
# Run as HTTP server instead of stdio
gabs run --gameId mygame --target "/path/to/game" --http "localhost:8080"
```

### Multiple Games

```bash
# Run different games simultaneously  
gabs run --gameId minecraft --target "/path/to/minecraft" --http ":8080"
gabs run --gameId rimworld --target "/path/to/rimworld" --http ":8081"  
```

### Process Management

```bash
# Start game but don't run MCP server yet
gabs start --gameId mygame --target "/path/to/game"

# Later, connect MCP to running game
gabs attach --gameId mygame

# Stop gracefully
gabs stop --gameId mygame

# Force kill if needed
gabs kill --gameId mygame

# Restart game
gabs restart --gameId mygame
```

## Build from Source

Requirements: Go 1.22+

```bash
# Build for your platform
go build ./cmd/gabs

# Build for all platforms
make build-all

# Or manually:
GOOS=darwin  GOARCH=arm64  go build -o dist/gabs-darwin-arm64 ./cmd/gabs
GOOS=linux   GOARCH=amd64  go build -o dist/gabs-linux-amd64  ./cmd/gabs  
GOOS=windows GOARCH=amd64  go build -o dist/gabs-windows-amd64.exe ./cmd/gabs
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

**Q: Do I need to modify my existing mod to use GABS?**  
A: Yes, your mod needs to implement the GABP protocol to communicate with GABS. This makes it "GABP compliant." But it's just a simple JSON API!

**Q: Can multiple AI tools connect at the same time?**  
A: Currently, one AI tool per game instance. Run multiple GABS instances for multiple AI connections.

**Q: Does this work with multiplayer games?**  
A: GABS connects to your local mod instance. Multiplayer compatibility depends on your mod's design.

**Q: Is this secure?**  
A: GABS only accepts local connections and uses token authentication. Your game never exposes ports to the internet.

**Q: What games are supported?**  
A: Any game where you can add GABP compliant mods! We have examples for Unity, C#/Harmony, and Java games.