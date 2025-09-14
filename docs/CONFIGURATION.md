# Configuration Guide

This guide shows you how to configure GABS to work with your games.

## Quick Setup

The easiest way to add games is with the interactive command:

```bash
gabs games add minecraft
```

This will ask you questions and set up everything automatically.

## Adding Games Step by Step

When you run `gabs games add [game-name]`, you'll be asked:

### 1. Game Name
A friendly name for your game (like "Minecraft Server" or "My RimWorld").

### 2. Launch Mode
How GABS should start your game:

- **DirectPath**: Point to the game's `.exe` file
- **SteamAppId**: Use Steam's App ID number (like `294100` for RimWorld)
- **EpicAppId**: Use Epic Games Store ID
- **CustomCommand**: Use a custom command with arguments

### 3. Target
The specific path, ID, or command based on your launch mode:
- For DirectPath: `/path/to/game.exe`
- For Steam: `294100` (the App ID)
- For Epic: The Epic App ID
- For Custom: Your complete command

### 4. Working Directory (Optional)
Where the game should run from. Leave blank to use the game's default location.

### 5. Stop Process Name (Required for Steam/Epic Games)
The name of the actual game process to stop when using games.stop or games.kill. **This is required for Steam/Epic games** to enable proper game termination. Without it, GABS can only stop the launcher process, not the actual game. Examples:
- For RimWorld: `RimWorldWin64.exe` (Windows) or `RimWorld` (Linux/macOS)
- For Minecraft with Java: `java`
- For Unity games: often the game name with `.exe` extension

**Note:** For `SteamAppId` and `EpicAppId` launch modes, you must provide a `stopProcessName` or GABS will not be able to properly terminate the game.

### 6. Game Configuration Complete

All games configured through GABS use **local GABP mode** for maximum security and simplicity. 

#### What is "Bridge Configuration"?
When you start a game via AI commands, GABS automatically creates **bridge configuration** - connection details that tell your game mod how to communicate with GABS:

- **Port**: Unique port number your mod should listen on (e.g., 12345)
- **Token**: Secure authentication token to verify connections
- **Game ID**: Your game's identifier for namespacing
- **Host**: Always localhost (127.0.0.1) for security

This configuration is provided to your game via:
1. **Environment variables** (recommended): `GABP_SERVER_PORT`, `GABP_TOKEN`, `GABS_GAME_ID`
2. **Bridge file** (fallback): `~/.gabs/{gameId}/bridge-{timestamp}.json`

**Key Point**: Your game mod acts as the GABP server (listening), while GABS acts as the GABP client (connecting).

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

Your games are saved in `~/.gabs/config.json`. Here's what it looks like:

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
Best for: Custom game installations or modded games
```json
{
  "launchMode": "DirectPath",
  "target": "/home/user/games/minecraft/start.sh",
  "workingDir": "/home/user/games/minecraft"
}
```

### SteamAppId
Best for: Games installed through Steam
```json
{
  "launchMode": "SteamAppId", 
  "target": "294100",
  "stopProcessName": "RimWorldWin64.exe"
}
```
You can find Steam App IDs on the game's Steam store page URL. **The `stopProcessName` is required** for Steam games to enable proper game termination.

### EpicAppId
Best for: Games installed through Epic Games Store
```json
{
  "launchMode": "EpicAppId",
  "target": "your-epic-app-id",
  "stopProcessName": "GameName.exe"
}
```
**The `stopProcessName` is required** for Epic games to enable proper game termination.

### CustomCommand
Best for: Complex launch setups or special requirements
```json
{
  "launchMode": "CustomCommand",
  "target": "java -Xmx4G -jar server.jar --nogui",
  "workingDir": "/opt/minecraft"
}
```

## GABP Communication

GABS uses **local-only GABP communication** for security and simplicity. Here's how it works:

### Local Communication (Current Implementation)
- Game mods connect to GABS on localhost (127.0.0.1) only
- Each game gets a unique port and secure token
- Bridge configuration stored in `~/.gabs/{gameId}/bridge.json`
- Maximum security with no network exposure

### Bridge Configuration
When you start a game, GABS creates a bridge configuration file that looks like this:

```json
{
  "port": 49234,
  "token": "a1b2c3d4e5f6...",
  "gameId": "minecraft"
}
```

Your game mod reads this file to establish a secure connection with GABS.

## Tool Normalization Configuration

GABS can automatically normalize MCP tool names to be compatible with different AI platforms, particularly OpenAI's API which has strict naming requirements.

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

GABS now supports better game stopping through the optional `stopProcessName` configuration. This addresses the limitation where Steam/Epic launcher games could only have their launcher process stopped, not the actual game.

### How It Works

When you configure a `stopProcessName`, GABS will:
1. First try to find and stop processes with that name
2. If no processes are found with that name, fall back to stopping the launched process (if any)
3. Support both graceful termination (games.stop) and force killing (games.kill)

### Platform Support

The process finding works across platforms:
- **Windows**: Uses `tasklist` and `taskkill` commands
- **macOS**: Uses `ps` command with process matching
- **Linux**: Uses `ps` command with process matching

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

**Important:** For launcher-based games (`SteamAppId` and `EpicAppId`), the `stopProcessName` field is mandatory. GABS will refuse to save configurations for these launch modes without it, as proper game termination would not be possible.

## Troubleshooting

### "Game won't start"
1. Check that your target path or ID is correct
2. Make sure the game is installed
3. Try running the launch command manually first

### "Can't connect to game mod"
1. Make sure your game mod supports GABP
2. Check that the mod is listening on the right port
3. Verify GABP mode settings match between GABS and your mod

### "Configuration not found"
The config file is created automatically when you add your first game. If it's missing, run `gabs games add` to create a new one.