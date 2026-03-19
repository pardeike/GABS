# AI Client Setup Guide

This guide assumes you downloaded one of the GABS release archives from the
GitHub Releases page and want to get from "zip file" to "AI can control my
games" as quickly as possible.

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

By default, GABS stores its configuration in:

- `~/.gabs/config.json`

Per-game runtime files for running games are written under:

- `~/.gabs/<gameId>/bridge.json`
- `~/.gabs/<gameId>/runtime.json` (internal ownership tracking used by GABS)

If you need a complete config example, see `example-config.json` in the release
archive.

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

Each live Codex session will start its own stdio GABS process. That is
intentional. GABS coordinates shared ownership per game, not with a machine-wide
`gabs server` singleton.

### Generic MCP Clients

If your MCP client supports stdio servers, the essential configuration is:

```json
{
  "command": "/absolute/path/to/gabs",
  "args": ["server"]
}
```

The important part is that the client launches the GABS binary with the
`server` subcommand.

### OpenAI-Style Tool Calling Clients

Some OpenAI-style clients prefer stricter tool names than MCP itself requires.
If that applies to your client, enable tool normalization in
`~/.gabs/config.json`:

```json
{
  "toolNormalization": {
    "enableOpenAINormalization": true,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  }
}
```

This converts mirrored tool names like `minecraft.inventory.get` into
OpenAI-compatible forms such as `minecraft_inventory_get`.

See also:

- `docs/OPENAI_TOOL_NORMALIZATION.md`

## Optional HTTP Mode

If your tooling prefers HTTP instead of stdio:

```bash
gabs server --http localhost:8080
```

Then talk to the MCP endpoint over HTTP:

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

If you have more than one live GABS session, the second session will not try to
launch or connect to the same game again by default. To intentionally move a
running game's ownership to the current session, use:

```json
{
  "gameId": "rimworld",
  "forceTakeover": true
}
```

with `games.connect`.

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
