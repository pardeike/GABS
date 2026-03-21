# GABS - Game Agent Bridge Server

Compatible with **GABP v1.1** on wire major **`gabp/1`**.

GABS lets MCP-capable AI tools start games, check status, and call tools exposed
by GABP-compatible game mods.

If you are installing GABS from a release archive, start with **Quick Start**
below. If you want the full copy-paste setup guide, read the
[AI Client Setup Guide](docs/AI_CLIENT_SETUP.md).

## Why GABS?

GABS is aimed at developers who want a practical way to connect local AI tools
to real games.

- Configure games once with `gabs games add`
- Keep everything local by default
- Work with Claude Desktop, Codex CLI, and other MCP clients
- Support direct executables, Steam App IDs, Epic App IDs, and custom commands
- Mirror game-specific mod tools into MCP when the mod connects

## Quick Start

### 1. Download and verify the binary

Download the latest release bundle for your system from
[GitHub Releases](releases/latest).

Available archives are named like:
- **Windows x64**: `gabs-<version>-windows-amd64.zip`
- **macOS Apple Silicon**: `gabs-<version>-darwin-arm64.zip`
- **macOS Intel**: `gabs-<version>-darwin-amd64.zip`
- **Linux x64**: `gabs-<version>-linux-amd64.zip`
- **Linux ARM64**: `gabs-<version>-linux-arm64.zip`

Each archive contains:
- the `gabs` binary (`gabs.exe` on Windows)
- `README.md`
- the full `docs/` folder
- `example-config.json`
- `LICENSE`

After unzipping:

**Windows**
```powershell
.\gabs.exe version
```

**macOS / Linux**
```bash
chmod +x gabs
./gabs version
```

### 2. Add a game

```bash
# Interactive setup
gabs games add minecraft

# See saved game IDs
gabs games list

# Show one game's saved config
gabs games show minecraft
```

The setup is interactive. In most cases you only need to answer:

- **Game name**: a label you recognize
- **Launch mode**: direct path, Steam App ID, Epic App ID, or custom command
- **Target**: the executable path or store App ID
- **Stop process name**: the real game process name used by `games.stop` and
  `games.kill`

For Steam and Epic games, `stopProcessName` is required. Example values:
`RimWorldWin64.exe`, `RimWorld`, or `java`.

### 3. Add GABS to your AI client

Paste one of these into your AI client's MCP config.

**Claude Desktop:**
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

**Codex CLI:**
```toml
[mcp_servers.gabs]
command = "/absolute/path/to/gabs"
args = ["server"]
```

**Generic MCP client:**
```json
{
  "command": "/absolute/path/to/gabs",
  "args": ["server"]
}
```

If your client uses strict OpenAI-style tool naming, enable
`toolNormalization` in `~/.gabs/config.json`. See
[OpenAI Tool Normalization](docs/OPENAI_TOOL_NORMALIZATION.md).

### 4. Try these prompts

- "List my games"
- "Start RimWorld"
- "Show the status of all games"
- "Stop Minecraft"

If you want a download-to-working walkthrough, use the
[AI Client Setup Guide](docs/AI_CLIENT_SETUP.md).

## Common Setup Notes

- **Steam/Epic stopping**: use the real game process name, not the launcher
  name.
- **More than one AI session**: that is fine. GABS coordinates ownership per
  game so two live sessions do not both launch or attach to the same game by
  accident.
- **Game mod cannot find bridge config**: the mod should first read
  `GABP_SERVER_PORT`, `GABP_TOKEN`, and `GABS_GAME_ID`, and only fall back to
  `GABS_BRIDGE_PATH` or `~/.gabs/<gameId>/bridge.json`.

## How It Works

Most users only need this mental model:

- Your AI client starts `gabs server`
- GABS starts or attaches to your game
- If the game mod speaks GABP, GABS mirrors that mod's tools into MCP

Architecture details matter mainly if you are writing a mod or debugging a
bridge issue.

![GABS Architecture](docs/architecture-flow.svg)

```
AI Agent ← MCP → GABS ← GABP Client → GABP Server (Game Mod) ← Game API → Game
```

In the GABP layer, your mod is the server and GABS is the client.

## Common MCP Tools

Most users only need a few tools at first:

- **`games.list`** - List configured game IDs
- **`games.show`** - Show one saved game config
- **`games.start`** - Start a game
- **`games.stop`** - Stop a game gracefully
- **`games.kill`** - Force stop a game
- **`games.status`** - Check if a game is running
- **`games.connect`** - Reconnect to a running game's mod bridge
- **`games.tool_names`** - List mirrored game-specific tools after a mod connects
- **`games.tool_detail`** - Show the schema for one mirrored tool
- **`games.call_tool`** - Call a mirrored tool through the stable core surface

For the full MCP surface and advanced behavior, see the
[AI Integration Guide](docs/INTEGRATION.md).

## Game-Specific Tools from Mods

When a GABP-compatible mod connects, GABS mirrors the mod's canonical tool
names into MCP-friendly names such as `minecraft.inventory.get` or
`rimworld.crafting.build`.

The usual discovery flow is:
```
AI: "Reconnect to RimWorld and show me its mod tools"
GABS: games.connect {"gameId": "rimworld"}
GABS: games.tool_names {"gameId": "rimworld", "brief": true}
GABS: games.tool_detail {"tool": "rimworld.crafting.build"}
```

Most users can ignore attention gating, resource mirroring, and protocol
details until they need them. Those topics are covered in the dedicated docs.

## Documentation

- **[AI Client Setup Guide](docs/AI_CLIENT_SETUP.md)** - Install a release bundle and connect Claude Desktop, Codex CLI, or generic MCP clients
- **[Configuration Guide](docs/CONFIGURATION.md)** - Detailed setup for different game types and tool normalization
- **[AI Integration Guide](docs/INTEGRATION.md)** - Connect GABS to different AI tools and deployment scenarios
- **[Mod Development Guide](docs/MOD_DEVELOPMENT.md)** - Add GABP support to your game mods
- **[Advanced Usage Guide](docs/ADVANCED_USAGE.md)** - Multiple instances, HTTP mode, scripting, and more
- **[Deployment Guide](docs/DEPLOYMENT.md)** - Production deployments and cloud setups
- **[OpenAI Tool Normalization](docs/OPENAI_TOOL_NORMALIZATION.md)** - Configure tool name compatibility for OpenAI API
- **[Dynamic Tools Guide](docs/DYNAMIC_TOOLS_GUIDE.md)** - How AI agents handle expanding tool sets
- **[AI Dynamic Tools FAQ](docs/AI_DYNAMIC_TOOLS_FAQ.md)** - Common questions about dynamic tool discovery

## For Mod Developers

Want your game to work with GABS? Add GABP support to your mod:

1. **Read GABP configuration** from environment variables when your game starts:
   - `GABS_GAME_ID` - Your game's identifier
   - `GABP_SERVER_PORT` - Port your mod should listen on
   - `GABP_TOKEN` - Authentication token for GABS connections
   - `GABS_BRIDGE_PATH` - Optional `bridge.json` fallback/debug path
2. **Start a local GABP server** to listen for GABS connections (your mod = server, GABS = client)
3. **Implement the current GABP runtime methods** (`session/hello`, `tools/list`, `tools/call`) or use the official `gabp-runtime` library so your schemas match what GABS expects
   - For GABP v1.1 bridges, advertise optional attention support through capabilities before exposing `attention/current`, `attention/ack`, and the attention lifecycle channels
4. **Expose game features** as tools, resources, and events using canonical GABP tool names such as `inventory/get` or `core/ping`

See the [Mod Development Guide](docs/MOD_DEVELOPMENT.md) for complete examples in C#, Java, and Python.

## Build from Source

Requirements: Go 1.22+

```bash
# Simple build
go build ./cmd/gabs

# Build with version information (recommended)
make build

# Build with custom version
go build -ldflags "-X github.com/pardeike/gabs/internal/version.Version=vX.Y.Z" ./cmd/gabs
```

## Contributing & Support

- **Issues & Ideas**: [GitHub Issues](issues)
- **GABP Protocol**: [GABP Repository](https://github.com/pardeike/GABP)
- **Example Configuration**: See `example-config.json` for sample configurations

## License

MIT License - see [LICENSE](LICENSE) for details.

---

*GABS makes AI-game interaction simple. Configure once, control naturally.*
