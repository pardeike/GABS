# Configuration Guide

This guide shows you how to add games to GABS and what the important config
fields mean.

## Quick Setup

For most users, this is the only command you need:

```bash
gabs games add minecraft
```

GABS will ask a few questions and save the result to your local config file.

## What GABS Asks You

When you run `gabs games add <game-id>`, GABS asks for:

### 1. Game Name
A friendly label such as `Minecraft Server` or `RimWorld`.

### 2. Launch Mode
How GABS should start the game:

- **DirectPath**: a local executable or script
- **SteamAppId**: a Steam App ID such as `294100` for RimWorld
- **EpicAppId**: Use Epic Games Store ID
- **CustomCommand**: Use a custom command with arguments

### 3. Target
The executable path, App ID, or command for the selected launch mode:

- For DirectPath: `/path/to/game.exe`
- For Steam: `294100` (the App ID)
- For Epic: The Epic App ID
- For Custom: Your complete command

### 4. Working Directory (Optional)
Where the game should run from. Leave blank to use the game's default location.

### 5. Stop Process Name
The real game process name used by `games.stop` and `games.kill`.

This is required for Steam and Epic launch modes. Without it, GABS can launch
the game but cannot stop the actual game process reliably.

Examples:
- For RimWorld: `RimWorldWin64.exe` (Windows) or `RimWorld` (Linux/macOS)
- For Minecraft with Java: `java`
- For Unity games: often the game name with `.exe` extension

### 6. Save and verify

After setup, verify the saved config:

```bash
gabs games list
gabs games show minecraft
```

## What Most Users Need To Know

- GABS stores your games in `~/.gabs/config.json`
- GABS starts games using the launch data you entered above
- If the game also has a GABP-compatible mod, GABS can connect to that mod and
  mirror game-specific tools into MCP

## Optional: How GABS Talks To Your Mod

Most users can skip this section.

GABS uses local-only GABP communication for security and simplicity.

When you start a game, GABS passes bridge configuration to the mod through:

1. Environment variables: `GABP_SERVER_PORT`, `GABP_TOKEN`, `GABS_GAME_ID`
2. A bridge file fallback: `~/.gabs/{gameId}/bridge.json`

In the GABP layer, your game mod listens for the connection and GABS connects
to it.

GABS also keeps an internal ownership record at `~/.gabs/{gameId}/runtime.json`
so separate GABS sessions do not accidentally launch or attach to the same game
at the same time. Mods should ignore `runtime.json`; it is for GABS itself.

## Managing Your Games

### View All Games
```bash
gabs games list
```
Shows all configured games and their current status.

### View Game Details
```bash
gabs games show minecraft
```
Shows complete configuration for one game.

### Remove a Game
```bash
gabs games remove minecraft
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
  "games": {
    "minecraft": {
      "id": "minecraft",
      "name": "Minecraft Server",
      "launchMode": "DirectPath",
      "target": "/opt/minecraft/start.sh",
      "workingDir": "/opt/minecraft",
      "stopProcessName": "java",
      "description": "Main Minecraft server"
    },
    "rimworld": {
      "id": "rimworld", 
      "name": "RimWorld",
      "launchMode": "SteamAppId",
      "target": "294100",
      "stopProcessName": "RimWorldWin64.exe"
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
  "target": "/home/user/games/minecraft/start.sh",
  "workingDir": "/home/user/games/minecraft"
}
```

### SteamAppId
Best for games installed through Steam.
```json
{
  "launchMode": "SteamAppId", 
  "target": "294100",
  "stopProcessName": "RimWorldWin64.exe"
}
```
You can find the App ID in the game's Steam store URL. `stopProcessName` is
required for Steam games.

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

### CustomCommand
Best for complex launch setups or special requirements.
```json
{
  "launchMode": "CustomCommand",
  "target": "java -Xmx4G -jar server.jar --nogui",
  "workingDir": "/opt/minecraft"
}
```

## GABP Communication Reference

This section is mainly useful if you are writing or debugging a mod bridge.

GABS uses local-only GABP communication.

### Local Communication (Current Implementation)
- GABS connects to game mods on localhost (`127.0.0.1`) only
- Each game gets a unique port and token
- Bridge configuration is also written to `~/.gabs/{gameId}/bridge.json`

### Bridge Configuration
When you start a game, GABS creates a bridge configuration file that looks like this:

```json
{
  "port": 49234,
  "token": "a1b2c3d4e5f6...",
  "gameId": "minecraft"
}
```

Mods can read this file, but environment variables remain the preferred source.

## Shared Runtime Ownership

When a game is already starting or running, GABS writes a per-game
`runtime.json` file so other live GABS sessions can see that the game already
has an owner.

- A second `games.start` returns immediately with "already starting" or
  "already running" instead of launching a second copy
- A second `games.connect` also returns immediately instead of waiting for a
  competing bridge connection
- `games.status` can report that another GABS session owns the process

If you intentionally want a different GABS session to take over a running game,
use `games.connect` with `forceTakeover: true`.

## Tool Normalization Configuration

Only use this if your AI platform has strict tool naming rules, such as the
OpenAI API.

### Tool Normalization Options

The `toolNormalization` section supports these options:

- **`enableOpenAINormalization`** (boolean): Enable/disable OpenAI-compatible normalization (default: `false`)
  - Replaces dots (.) with underscores (_) in tool names
  - Enforces 64-character length limit
  - Ensures tool names start with a letter
- **`maxToolNameLength`** (integer): Maximum length for tool names (default: `64`)
- **`preserveOriginalName`** (boolean): Store original name in tool description/metadata (default: `true`)

### Example Configuration

```json
{
  "version": "1.0",
  "toolNormalization": {
    "enableOpenAINormalization": true,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  },
  "games": {
    // ... your game configurations
  }
}
```

### When to Use OpenAI Normalization

Enable this feature when:
- Using GABS with OpenAI's API directly
- Your game mods use dotted tool names (e.g., `minecraft.inventory.get`)
- You need strict OpenAI API compliance

**Example transformations:**
- `minecraft.inventory.get` → `minecraft_inventory_get`
- `rimworld.crafting.build` → `rimworld_crafting_build`

For complete details about tool normalization, see the [OpenAI Tool Normalization Guide](OPENAI_TOOL_NORMALIZATION.md).

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
| RimWorld | Windows | `RimWorldWin64.exe` |
| RimWorld | macOS/Linux | `RimWorld` |
| Minecraft (Java) | All | `java` |
| Unity Games | Windows | `GameName.exe` |
| Steam Games | All | Check game's install directory |

### Configuration Examples

```json
{
  "games": {
    "rimworld-steam": {
      "launchMode": "SteamAppId",
      "target": "294100", 
      "stopProcessName": "RimWorldWin64.exe"
    },
    "minecraft-server": {
      "launchMode": "DirectPath",
      "target": "/opt/minecraft/start.sh",
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
mandatory.

## Troubleshooting

### "Game won't start"
1. Check that your target path or ID is correct
2. Make sure the game is installed
3. Try running the launch command manually first

### "Can't connect to game mod"
1. Make sure your game mod supports GABP
2. Check that the mod is listening on the right port
3. Verify the mod is using `GABP_SERVER_PORT`, `GABP_TOKEN`, and `GABS_GAME_ID`
   from the environment or `bridge.json`

### "Configuration not found"
The config file is created automatically when you add your first game. If it's missing, run `gabs games add` to create a new one.
