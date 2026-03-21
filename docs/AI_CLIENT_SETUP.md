# AI Client Setup Guide

This guide is for people who downloaded a GABS release archive and want to go
from "zip file" to "my AI can start and manage my game" with as little setup
friction as possible.

## Fast Path

If you only want the short version:

1. Unzip the release archive.
2. Run `gabs version` to verify the binary works.
3. Run `gabs games add <game-id>`.
4. Paste the GABS config snippet into Claude Desktop, Codex CLI, or another MCP
   client.
5. Ask your AI to list or start your games.

The rest of this guide shows the exact commands and config snippets.

## What Is In The Release Archive

Each platform archive includes:

- the `gabs` binary (`gabs.exe` on Windows)
- `README.md`
- the full `docs/` folder
- `example-config.json`
- `LICENSE`

Archive names follow this pattern:

- `gabs-<version>-windows-amd64.zip`
- `gabs-<version>-darwin-arm64.zip`
- `gabs-<version>-darwin-amd64.zip`
- `gabs-<version>-linux-amd64.zip`
- `gabs-<version>-linux-arm64.zip`

## Install The Binary

### Windows

1. Unzip the archive to a stable folder, for example `C:\Tools\GABS`.
2. Open PowerShell in that folder.
3. Verify the binary:

```powershell
.\gabs.exe version
```

Use the full path in your AI configuration, for example
`C:\Tools\GABS\gabs.exe`.

### macOS and Linux

1. Unzip the archive to a stable folder.
2. Mark the binary as executable:

```bash
chmod +x gabs
./gabs version
```

Use the full path in your AI configuration, for example
`/Users/you/Tools/gabs` or `/opt/gabs/gabs`.

## Configure Your Games

Run the interactive setup once for each game:

```bash
gabs games add rimworld
gabs games add minecraft
```

Then verify the saved configuration:

```bash
gabs games list
gabs games show rimworld
```

During setup, the important fields are:

- **Launch mode**: how GABS starts the game
- **Target**: the executable path or store App ID
- **Stop process name**: the real game process name used for stopping the game

Examples of `stopProcessName`:

- RimWorld on Windows: `RimWorldWin64.exe`
- RimWorld on macOS/Linux: `RimWorld`
- Java-based Minecraft setups: `java`

For Steam and Epic launch modes, `stopProcessName` is required. Without it,
GABS can launch the game but cannot stop the real game process reliably.

## What Success Looks Like

After setup, these commands should work:

```bash
gabs games list
gabs games show rimworld
gabs version
```

If they do, your local GABS setup is in good shape.

## Configure Your AI Client

### Claude Desktop

Add GABS to your Claude Desktop MCP configuration:

```json
{
  "mcpServers": {
    "gabs": {
      "command": "/absolute/path/to/gabs",
      "args": ["server"]
    }
  }
}
```

On Windows, point `command` to `gabs.exe`.

### Codex CLI

Add GABS to your Codex CLI configuration:

```toml
[mcp_servers.gabs]
command = "/absolute/path/to/gabs"
args = ["server"]
```

On Windows, use the full path to `gabs.exe`.

Each live Codex session starts its own stdio GABS process. That is normal.
GABS coordinates ownership per game so two live AI sessions do not both launch
or attach to the same game by accident.

### Generic MCP Clients

If your MCP client supports stdio servers, the essential configuration is:

```json
{
  "command": "/absolute/path/to/gabs",
  "args": ["server"]
}
```

The key point is simple: your AI client should launch the `gabs` binary with
the `server` subcommand.

### OpenAI-Style Tool Calling Clients

Only do this if your client has strict OpenAI-style tool naming rules.

Enable tool normalization in `~/.gabs/config.json`:

```json
{
  "toolNormalization": {
    "enableOpenAINormalization": true,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  }
}
```

This turns tool names like `minecraft.inventory.get` into
`minecraft_inventory_get`.

See also:

- `docs/OPENAI_TOOL_NORMALIZATION.md`

## Optional HTTP Mode

Most users do not need this. Use it only when your tooling wants HTTP instead
of stdio.

```bash
gabs server --http localhost:8080
```

Then send MCP requests to the HTTP endpoint:

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

## First Commands To Try

Once your AI client is connected, try prompts like:

- "List my configured games"
- "Start RimWorld"
- "Show the status of all games"
- "Reconnect to RimWorld and list its tools"

If you have more than one live GABS session, the second session will not launch
or connect to the same game again by default. To intentionally move ownership
to the current session, use:

```json
{
  "gameId": "rimworld",
  "forceTakeover": true
}
```

with `games.connect`.

## Where GABS Stores Files

Most users do not need this section, but it helps when debugging.

GABS stores its main config in:

- `~/.gabs/config.json`

Per-game runtime files live under:

- `~/.gabs/<gameId>/bridge.json`
- `~/.gabs/<gameId>/runtime.json`

`runtime.json` is internal ownership tracking used by GABS itself.

## Troubleshooting

### The AI Cannot See Any GABS Tools

1. Make sure the binary path in the AI config is correct.
2. Run `gabs server` manually in a terminal and look for startup errors.
3. Restart the AI client after editing its MCP configuration.

### The Game Starts But Cannot Be Stopped

Check `stopProcessName` with:

```bash
gabs games show <game-id>
```

Launcher-based games such as Steam and Epic titles need the actual game process
name, not just the launcher.

### A Mod Cannot Find `bridge.json`

Make sure the mod first checks the environment variables:

- `GABP_SERVER_PORT`
- `GABP_TOKEN`
- `GABS_GAME_ID`

and only falls back to:

- `~/.gabs/<gameId>/bridge.json`

or the `GABS_BRIDGE_PATH` environment variable when present.
