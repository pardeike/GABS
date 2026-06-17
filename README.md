# GABS - Game Agent Bridge Server

Compatible with **GABP v1.1** on wire major **`gabp/1`**.

GABS lets MCP-capable AI tools start games, check status, and call tools exposed
by GABP-compatible game integrations.

If you are installing GABS from a release archive, start with **Quick Start**
below. If you want the full copy-paste setup guide, read the
[AI Client Setup Guide](docs/AI_CLIENT_SETUP.md).

## Why GABS?

GABS is aimed at developers who want a practical way to connect local AI tools
to real games.

- Configure games once with `gabs games add`
- Keep everything local by default
- Work with Claude Desktop, Codex CLI, and other MCP clients
- Support direct executables, managed Steam games, Epic App IDs, and custom commands
- Mirror game-specific tools into MCP when the bridge connects

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
gabs games add factory

# See saved game IDs
gabs games list

# Show one game's saved config
gabs games show factory
```

The setup is interactive. In most cases you only need to answer:

- **Game name**: a label you recognize
- **Launch mode**: direct path, managed Steam, Epic App ID, or custom command
- **Target**: the executable path or store App ID
- **Stop process name**: the real game process name used by `games.stop` and
  `games.kill`

For `SteamManaged`, GABS resolves the Steam app to the installed executable and
starts Steam if needed. For launcher URL modes such as `SteamAppId` and
`EpicAppId`, `stopProcessName` is required. Example values: `GameName.exe`,
`AdventureGame`, or `java`.

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
If your client disconnects after `tools/list` because it rejects a public tool's
`outputSchema` fields, set `stripOutputSchema` to `true`. See the
[Configuration Guide](docs/CONFIGURATION.md). Mirrored game tools are discovered
through `games_tool_names` and are not advertised in the public `tools/list`
response.
If a game or bridge starts slowly, tune the total background connection budget
with the `timeouts.startup` section described in the
[Configuration Guide](docs/CONFIGURATION.md). `games_start` still returns after a
bounded initial wait so MCP clients do not time out while the game keeps loading.

### 4. Try these prompts

- "List my games"
- "Start AdventureGame"
- "Show the status of all games"
- "Stop FactorySim"

If you want a download-to-working walkthrough, use the
[AI Client Setup Guide](docs/AI_CLIENT_SETUP.md).

## Common Setup Notes

- **Steam/Epic stopping**: use the real game process name, not the launcher
  name.
- **Steam bridge games**: prefer `SteamManaged` launch. It resolves the Steam
  app manifest to the installed game executable, starts Steam if needed, and
  launches the real game process with GABP environment variables. Use
  `gabs games doctor <id>` to inspect a config and `gabs games repair <id>` to
  convert an older `SteamAppId` launcher URL config.
- **More than one AI session**: that is fine. GABS coordinates ownership per
  game with a short active-owner lease. You can hop between live sessions:
  `games_connect` takes over naturally after the previous session goes idle,
  while active game-bound calls are still protected from competing sessions.
- **Game bridge cannot find GABP configuration**: the game-side bridge should read
  `GABP_SERVER_PORT`, `GABP_TOKEN`, and `GABS_GAME_ID` from its process
  environment. The `bridge.json` file is GABS' endpoint cache/debug artifact,
  not runtime input for game-side bridge code.
- **Stopped game still has `bridge.json`**: this is normal. Treat it as an
  endpoint cache/debug artifact, not a recovery target.
- **Start says the endpoint cache port is already in use**: use
  `games_connect` if an existing game-side bridge owns that endpoint. Use
  `games_start` with `resetEndpoint: true` only after confirming the cache
  should be rotated for a new process.
- **Confusing bridge state**: start with `games_status`. It compares the
  runtime state and process environment where the OS allows it. If a Steam
  launcher URL config reused old GABP environment, run
  `gabs games repair <id>` to switch to managed Steam launch.

## How It Works

Most users only need this mental model:

- Your AI client starts `gabs server`
- GABS starts or attaches to your game
- If the game-side bridge speaks GABP, GABS mirrors that bridge's tools into MCP

Architecture details matter mainly if you are writing a game-side bridge or debugging a
bridge issue.

![GABS Architecture](docs/architecture-flow.svg)

```
AI Agent ← MCP → GABS ← GABP Client → GABP Server (Game Bridge) ← Game API → Game
```

In the GABP layer, your game-side bridge is the server and GABS is the client.

## Common MCP Tools

Most users only need a few tools at first. Release builds expose strict-safe MCP
tool names by default because some clients reject dots in tool names:

- **`games_list`** - List configured game IDs
- **`games_show`** - Show one saved game config
- **`games_start`** - Start a game
- **`games_stop`** - Stop a game gracefully
- **`games_kill`** - Force stop a game
- **`games_status`** - Check if a game is running
- **`games_connect`** - Reconnect to a running game's game-side bridge
- **`games_tool_names`** - List mirrored game-specific tools after a bridge connects
- **`games_tool_detail`** - Show the schema for one mirrored tool
- **`games_call_tool`** - Call a connected game tool through the stable core surface

The older dotted names such as `games.list` and `games.call_tool` remain accepted
as call aliases, but `tools/list` advertises strict-safe names unless you
explicitly disable normalization in config.

For the full MCP surface and advanced behavior, see the
[AI Integration Guide](docs/INTEGRATION.md).

## Game-Specific Tools from Bridges

When a GABP-compatible bridge connects, GABS mirrors the bridge's canonical
slash-delimited GABP tool names into strict-safe names such as
`factory_inventory_get` or `adventure_crafting_build`. The public `tools/list`
response stays core-only so clients do not churn on large game-specific tool
sets. Use `games_tool_names` for discovery, `games_tool_detail` for one schema,
and `games_call_tool` for the actual call. Direct mirrored MCP tools may still
be callable when you already know the name, but agents should not depend on
dynamic `tools/list` refreshes.

The usual discovery flow is:
```
AI: "Reconnect to AdventureGame and show me its game-specific tools"
GABS: games_connect {"gameId": "adventure"}
GABS: games_tool_names {"gameId": "adventure", "brief": true}
GABS: games_tool_detail {"tool": "adventure_crafting_build"}
```

Most users can ignore attention gating, resource mirroring, and protocol
details until they need them. Those topics are covered in the dedicated docs.

## Documentation

- **[AI Client Setup Guide](docs/AI_CLIENT_SETUP.md)** - Install a release bundle and connect Claude Desktop, Codex CLI, or generic MCP clients
- **[Configuration Guide](docs/CONFIGURATION.md)** - Detailed setup for different game types and tool normalization
- **[AI Integration Guide](docs/INTEGRATION.md)** - Connect GABS to different AI tools and deployment scenarios
- **[GABP Bridge Development Guide](docs/GABP_BRIDGE_DEVELOPMENT.md)** - Add GABP support to your game integrations
- **[Advanced Usage Guide](docs/ADVANCED_USAGE.md)** - Multiple instances, HTTP mode, scripting, and more
- **[Deployment Guide](docs/DEPLOYMENT.md)** - Production deployments and cloud setups
- **[OpenAI Tool Normalization](docs/OPENAI_TOOL_NORMALIZATION.md)** - Configure tool name compatibility for OpenAI API
- **[Dynamic Tools Guide](docs/DYNAMIC_TOOLS_GUIDE.md)** - How AI agents handle expanding tool sets
- **[AI Dynamic Tools FAQ](docs/AI_DYNAMIC_TOOLS_FAQ.md)** - Common questions about dynamic tool discovery

## For Bridge Developers

Want your game to work with GABS? Add GABP support to your game-side bridge:

1. **Read GABP configuration** from environment variables when your game starts:
   - `GABS_GAME_ID` - Your game's identifier
   - `GABP_SERVER_PORT` - Port your game-side bridge should listen on
   - `GABP_TOKEN` - Authentication token for GABS connections
   The `bridge.json` file is GABS' endpoint cache/debug artifact. Do not read
   it as game-side runtime configuration.
2. **Start a local GABP server** to listen for GABS connections (your game-side bridge = server, GABS = client)
3. **Implement the current GABP runtime methods** (`session/hello`, `tools/list`, `tools/call`) or use the official `gabp-runtime` library so your schemas match what GABS expects
   - For GABP v1.1 bridges, advertise optional attention support through capabilities before exposing `attention/current`, `attention/ack`, and the attention lifecycle channels
4. **Expose game features** as tools, resources, and events using canonical GABP tool names such as `inventory/get` or `core/ping`

See the [GABP Bridge Development Guide](docs/GABP_BRIDGE_DEVELOPMENT.md) for complete examples in C#, Java, and Python.

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
