# Configuration Guide

This guide shows you how to add games to GABS and what the important config
fields mean.

## Quick Setup

For most users, this is the only command you need:

```bash
gabs games add factory
```

GABS will ask a few questions and save the result to your local config file.

## What GABS Asks You

When you run `gabs games add <game-id>`, GABS asks for:

### 1. Game Name
A friendly label such as `Example Game` or `AdventureGame`.

### 2. Launch Mode
How GABS should start the game:

- **DirectPath**: a local executable or script
- **SteamManaged**: a Steam App ID resolved to the installed game executable
- **SteamAppId**: a legacy Steam launcher URL for compatibility
- **EpicAppId**: Use Epic Games Store ID
- **CustomCommand**: Use a custom command with arguments

### 3. Target
The executable path, App ID, or command for the selected launch mode:

- For DirectPath: `/path/to/game.exe`
- For Steam: `123456` (the App ID)
- For Epic: The Epic App ID
- For Custom: Your complete command

### 4. Working Directory (Optional)
Where the game should run from. Leave blank to use the game's default location.

### 5. Stop Process Name
The real game process name used by `games.stop` and `games.kill`.

This is required for Steam and Epic launch modes. Without it, GABS can launch
the game but cannot stop the actual game process reliably.

Examples:
- For AdventureGame: `GameName.exe` (Windows) or `AdventureGame` (Linux/macOS)
- For FactorySim with Java: `java`
- For Unity games: often the game name with `.exe` extension

### 6. Save and verify

After setup, verify the saved config:

```bash
gabs games list
gabs games show factory
```

## What Most Users Need To Know

- GABS stores your games in `~/.gabs/config.json`
- GABS starts games using the launch data you entered above
- If the game also has a GABP-compatible bridge, GABS can connect to that bridge and
  mirror game-specific tools into MCP

## Optional: How GABS Talks To Your Game Bridge

Most users can skip this section.

GABS uses local-only GABP communication for security and simplicity.

When you start a game, GABS passes GABP connection data to the game-side bridge through:

1. Environment variables: `GABP_SERVER_PORT`, `GABP_TOKEN`, `GABS_GAME_ID`

The `bridge.json` file is GABS' endpoint cache/debug artifact. Game-side bridge
code should not read it as runtime configuration or use it as a fallback for
missing `GABP_*` environment values.

In the GABP layer, your game-side bridge listens for the connection and GABS connects
to it.

GABS also keeps an internal ownership record at `~/.gabs/{gameId}/runtime.json`
so separate GABS sessions do not accidentally launch or drive the same game at
the same time. Ownership is an expiring active-use lease, so you can hop
between live AI sessions by using `games_connect` after the previous session
goes idle. Game integrations should ignore `runtime.json`; it is for GABS
itself.

## Managing Your Games

### View All Games
```bash
gabs games list
```
Shows all configured games and their current status.

### View Game Details
```bash
gabs games show factory
```
Shows complete configuration for one game.

### Remove a Game
```bash
gabs games remove factory
```
Removes the game from your configuration.

## Configuration File

Your games are saved in `~/.gabs/config.json`.

Example:

The top-level `"version"` field below is the GABS config schema version, not
the GABP wire version.

```json
{
  "version": "1.0",
  "toolNormalization": {
    "enableOpenAINormalization": false,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  },
  "stripOutputSchema": false,
  "timeouts": {
    "startup": {
      "processStartSeconds": 10,
      "gabpConnectSeconds": 60
    }
  },
  "games": {
    "factory": {
      "id": "factory",
      "name": "Example Game",
      "launchMode": "DirectPath",
      "target": "/opt/factory/start.sh",
      "workingDir": "/opt/factory",
      "stopProcessName": "java",
      "description": "Main FactorySim server"
    },
    "adventure": {
      "id": "adventure",
      "name": "AdventureGame",
      "launchMode": "SteamManaged",
      "target": "123456"
    }
  }
}
```

## Launch Modes Explained

### DirectPath
Best for custom game installs, scripts, and local test setups.
```json
{
  "launchMode": "DirectPath",
  "target": "/home/user/games/factory/start.sh",
  "workingDir": "/home/user/games/factory"
}
```

### SteamManaged
Best for Steam games with GABP bridges.
```json
{
  "launchMode": "SteamManaged",
  "target": "123456"
}
```
You can find the App ID in the game's Steam store URL. GABS locates the Steam
library, reads the app manifest, starts the Steam client if needed, launches the
resolved executable with GABP environment variables, and prepares
`steam_appid.txt` when direct Steamworks startup requires it. Configured `args`
are passed to the game in this mode. If Steam or the platform relaunches the
final game process without inheriting those variables, `games_status` reports
`process-bridge-environment-missing`; use `DirectPath` or `CustomCommand` for a
wrapper that the final process actually inherits from.

Use `gabs games doctor <id>` to inspect the resolved executable and
`gabs games repair <id>` to switch an older `SteamAppId` config to this mode.

### SteamAppId
Legacy Steam launcher URL mode.
```json
{
  "launchMode": "SteamAppId",
  "target": "123456",
  "stopProcessName": "GameName.exe"
}
```
You can find the App ID in the game's Steam store URL. `stopProcessName` is
required for Steam games.

GABS starts Steam games through the platform launcher URL. Configured `args`
are not passed to the game in this mode. Put launch options such as
`-savedatafolder=...` in Steam's own launch options, or use `SteamManaged`,
`DirectPath`, or `CustomCommand` when GABS must control process arguments and
bridge environment directly.

In launcher-driven setups, an already-running platform launcher process can
prevent GABS from proving that new environment variables reached the real game
process. `games_status` reports whether the real game process environment is
readable. Prefer `SteamManaged` over launcher URL mode, but still verify the
final process environment. If it reports `process-bridge-environment-missing`,
use `DirectPath` or `CustomCommand`. For an existing Steam launcher config, run
`gabs games repair <id>`.

### EpicAppId
Best for games installed through Epic Games Store.
```json
{
  "launchMode": "EpicAppId",
  "target": "your-epic-app-id",
  "stopProcessName": "GameName.exe"
}
```
`stopProcessName` is required for Epic games.

As with Steam, configured `args` are not passed to the game in this mode. Use
the game launcher's own launch options, `DirectPath`, or `CustomCommand` for
process arguments.

### CustomCommand
Best for complex launch setups or special requirements.
```json
{
  "launchMode": "CustomCommand",
  "target": "java -Xmx4G -jar server.jar --nogui",
  "workingDir": "/opt/factory"
}
```

## GABP Communication Reference

This section is mainly useful if you are writing or debugging a game-side bridge.

GABS uses local-only GABP communication.

### Local Communication (Current Implementation)
- GABS connects to game integrations on localhost (`127.0.0.1`) only
- Each game gets a unique port and token
- GABS may write `~/.gabs/{gameId}/bridge.json` as an endpoint cache/debug artifact

### Bridge Configuration
When you start a game, GABS sends GABP configuration through environment
variables:

- `GABP_SERVER_PORT`
- `GABP_TOKEN`
- `GABS_GAME_ID`

Game integrations should read these environment variables directly.

## Shared Runtime Ownership

When a game is already starting or running, GABS writes a per-game
`runtime.json` file so other live GABS sessions can see whether the game
currently has an active owner.

- A second `games.start` returns immediately with "already starting" or
  "already running" instead of launching a second copy
- `games.connect` takes ownership naturally once the previous owner's lease is
  idle
- `games.connect` returns immediately with an active-owner result when another
  session is still inside its lease
- game-bound tool calls also check the lease before touching the bridge
- `games.status` reports the runtime owner, lease expiry, and bridge diagnostics

If you intentionally want a different GABS session to take over before the
active lease expires, use `games.connect` with `forceTakeover: true`.

If `games.start` reports `endpoint_cache_in_use`, the cached port is already
listening. Use `games.connect` if an already-running bridge owns that endpoint.
Use `games.start` with `resetEndpoint: true` only after confirming the cached
endpoint should be rotated for a new process.

## Tool Normalization Configuration

GABS exposes strict-safe MCP tool names by default. This keeps `tools/list`
accepted by clients that reject dotted names, including Claude variants seen in
the field. The configuration key keeps its historical name for compatibility.

### Tool Normalization Options

The `toolNormalization` section supports these options:

- **`enableOpenAINormalization`** (boolean): Enable/disable strict-safe MCP name normalization (default: `true` when `toolNormalization` is omitted)
  - Replaces dots, slashes, and other unsafe separators with underscores
  - Enforces 64-character length limit
- **`maxToolNameLength`** (integer): Maximum length for tool names (default: `64`)
- **`preserveOriginalName`** (boolean): Store original name in tool description/metadata (default: `true`)

Set `enableOpenAINormalization` to `false` only when you intentionally need the
old dotted MCP names in `tools/list`.

**Example transformations:**
- `games.call_tool` -> `games_call_tool`
- `factory.inventory.get` -> `factory_inventory_get`
- GABP `core/ping` for game `adventure` -> `adventure_core_ping`

Call aliases remain backward compatible: `games_call_tool`, `games.call_tool`,
qualified slash names, qualified dotted names, and discovered strict-safe names
all resolve without underscore guessing.

### Example Configuration

```json
{
  "version": "1.0",
  "toolNormalization": {
    "enableOpenAINormalization": true,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  },
  "stripOutputSchema": false,
  "games": {
    // ... your game configurations
  }
}
```

For complete details about tool normalization, see the [Tool Normalization Guide](OPENAI_TOOL_NORMALIZATION.md).

## Output Schema Stripping

Some MCP clients reject `tools/list` responses when a public tool includes an
`outputSchema` field. Mirrored game tools are discovered through
`games_tool_names`, but GABS can still omit output schemas from the public tool
list:

```json
{
  "stripOutputSchema": true
}
```

Default: `false`.

Use this for clients such as Claude Code if they disconnect with an
`outputSchema.type` validation error while reading `tools/list`.

This does not change tool execution and does not remove input schemas. Detailed
tool metadata, including output schema information, remains available through
`games_tool_detail`.

## Startup Timeout Configuration

If your game takes longer to appear in the process list or longer for its GABP
game-side bridge to start listening, you can override the startup process wait
and connection budget in `~/.gabs/config.json`.

### Startup Timeout Options

The `timeouts.startup` section supports these options:

- **`processStartSeconds`** (integer): How long GABS waits for the launched game
  process to become detectable in the OS process list (default: `10`)
- **`gabpConnectSeconds`** (integer): The total connection budget for the game
  GABP server to become available (default: `60`). `games_start` waits only for
  a bounded initial slice of that budget, returns before MCP clients hit their
  own tool-call timeout, and continues connecting in the background. You can
  also pass a one-off `timeout` argument to `games_start` for unusually slow
  bridge startup without changing the saved configuration.

The `timeouts.session` section supports:

- **`ownerLeaseSeconds`** (integer): How long an idle GABS session remains the
  active runtime owner after a normal game-bound action (default: `30`). Long
  game-bound calls extend the lease to cover their requested timeout plus a
  small safety margin, so this value controls roaming between idle sessions,
  not the maximum duration of a running command.

`games_start` only waits for an initial GABP handshake window. If the game is
still loading, GABS keeps trying in the background for the remaining startup
budget. Mirroring the connected bridge's full tool list can continue briefly in
the background. The public `tools/list` response stays stable, and known startup
commands can be sent immediately through `games_call_tool` while discovery tools
refresh.

### Example Configuration

```json
{
  "version": "1.0",
  "timeouts": {
    "startup": {
      "processStartSeconds": 20,
      "gabpConnectSeconds": 120
    },
    "session": {
      "ownerLeaseSeconds": 30
    }
  },
  "games": {
    // ... your game configurations
  }
}
```

## Improved Game Stopping

GABS can stop games more reliably when `stopProcessName` is set correctly.

When `stopProcessName` is configured, GABS will:
1. First try to find and stop processes with that name
2. If no processes are found with that name, fall back to stopping the launched process (if any)
3. Support both graceful termination (games.stop) and force killing (games.kill)

### Platform Support

The process finding works across platforms:
- **Windows**: Uses `tasklist` and `taskkill` commands
- **macOS**: Uses `pgrep` with standard process signals
- **Linux**: Uses `pgrep` with standard process signals

### Common Process Names

| Game | Platform | Process Name |
|------|----------|-------------|
| AdventureGame | Windows | `GameName.exe` |
| AdventureGame | macOS/Linux | `AdventureGame` |
| FactorySim (Java) | All | `java` |
| Unity Games | Windows | `GameName.exe` |
| Steam Games | All | Check game's install directory |

### Configuration Examples

```json
{
  "games": {
    "adventure-steam": {
      "launchMode": "SteamManaged",
      "target": "123456"
    },
    "factory-server": {
      "launchMode": "DirectPath",
      "target": "/opt/factory/start.sh",
      "stopProcessName": "java"
    },
    "epic-game": {
      "launchMode": "EpicAppId",
      "target": "epic-app-id",
      "stopProcessName": "GameName.exe"
    }
  }
}
```

For launcher-based games (`SteamAppId` and `EpicAppId`), `stopProcessName` is
mandatory. `SteamManaged` launches the resolved game executable directly, so
`stopProcessName` is optional.

## Troubleshooting

### "Game won't start"
1. Check that your target path or ID is correct
2. Make sure the game is installed
3. Run `gabs games doctor <id>`
4. Try running the launch command manually first

### "Can't connect to game-side bridge"
1. Make sure your game-side bridge supports GABP
2. Check that the game-side bridge is listening on the right port
3. Verify the game-side bridge is using `GABP_SERVER_PORT`, `GABP_TOKEN`, and `GABS_GAME_ID`
   from the environment
4. Run `games_status` and inspect `diagnostics.code`, `diagnostics.message`,
   and `nextActions`; it can identify stale runtime state, runtime ownership,
   and whether the real game process environment is readable.
5. If `diagnostics.code` is `process-bridge-environment-missing`, the running
   process is visible but cannot be attached through its environment. For Steam
   launcher URL configs, run `gabs games repair <id>` first; if managed launch
   still loses the environment, use `DirectPath` or `CustomCommand`.

### "Configuration not found"
The config file is created automatically when you add your first game. If it's missing, run `gabs games add` to create a new one.
