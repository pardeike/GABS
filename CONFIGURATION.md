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

### 5. GABP Mode
How GABS talks to your game mod:
- **local**: Game mod runs on the same computer (most common)
- **remote**: Game mod runs on a different computer
- **connect**: GABS connects to an existing mod server

### 6. GABP Host (For Remote Mode)
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
  "target": "294100"
}
```
You can find Steam App IDs on the game's Steam store page URL.

### EpicAppId
Best for: Games installed through Epic Games Store
```json
{
  "launchMode": "EpicAppId",
  "target": "your-epic-app-id"
}
```

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