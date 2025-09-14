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

### 6. GABP Mode
How GABS talks to your game mod:
- **local**: Game mod runs on the same computer (most common)
- **remote**: Game mod runs on a different computer
- **connect**: GABS connects to an existing mod server

### 7. GABP Host (For Remote Mode)
If using remote mode, enter the IP address where your game mod should listen.

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
  "games": {
    "minecraft": {
      "id": "minecraft",
      "name": "Minecraft Server",
      "launchMode": "DirectPath",
      "target": "/opt/minecraft/start.sh",
      "workingDir": "/opt/minecraft",
      "stopProcessName": "java",
      "gabpMode": "local",
      "description": "Main Minecraft server"
    },
    "rimworld": {
      "id": "rimworld", 
      "name": "RimWorld",
      "launchMode": "SteamAppId",
      "target": "294100",
      "stopProcessName": "RimWorldWin64.exe",
      "gabpMode": "local"
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

## GABP Modes Explained

### Local Mode (Default)
- Game mod listens on `127.0.0.1` (localhost only)
- Most secure option
- Best for development and single-machine setups

### Remote Mode
- Game mod listens on your specified IP address
- Allows AI running on different computers to connect
- Requires network configuration

### Connect Mode
- GABS connects to an already-running mod server
- Useful for persistent game servers
- Game mod must be started manually first

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