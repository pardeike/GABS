# Mod Development Guide

This guide helps you add GABP support to your game mods so they can work with GABS.

> **Prerequisites:** Understanding of [GABS Configuration](CONFIGURATION.md) and [AI Integration](INTEGRATION.md) recommended. For deployment scenarios, see [Deployment Guide](DEPLOYMENT.md).

## What is GABP?

[GABP](https://github.com/pardeike/GABP) (Game Agent Bridge Protocol) is a simple way for AI tools to talk to your game mods. Think of it like a translator between AI assistants and your game.

## Architecture Overview

**IMPORTANT: Understanding Client-Server Roles**

In GABP architecture:
- **Your mod = GABP Server** (listens for connections on a port)
- **GABS = GABP Client** (connects to your mod)

This is different from how GABS operates as an MCP server. The communication flow is:

```
AI Agent ← MCP → GABS ← GABP Client → GABP Server (Your Mod) ← Game API → Game
```

**Why this architecture?**
- GABS has better knowledge of which ports are available  
- Communication is always local (127.0.0.1) since GABS must launch the game
- GABS can connect to mods when they're ready, not the other way around
- Multiple game instances can run concurrently with unique ports

## Quick Overview

To work with GABS, your mod needs to:

1. **Read GABP server config** when the game starts (port to listen on, auth token)
2. **Act as GABP server** - listen for connections from GABS
3. **Expose your features** as tools, resources, and events

## Step 1: Reading GABP Server Configuration

When GABS starts your game, it passes GABP server configuration via environment variables:

### Essential Environment Variables

- `GABS_GAME_ID` - Your game's identifier 
- `GABP_SERVER_PORT` - Port number your mod should listen on (e.g., 12345)
- `GABP_TOKEN` - Authentication token for GABS connections

### Optional Environment Variables

- `GABS_BRIDGE_PATH` - Path to bridge.json file (for debugging/fallback only)

**Why Environment Variables?**
- ✅ Works reliably in concurrent game launches (each gets unique port)
- ✅ No file I/O required for essential configuration
- ✅ No permission or path issues
- ✅ Atomic - all info available instantly when game starts

### Bridge File (Optional Fallback)

If present, `GABS_BRIDGE_PATH` points to a JSON file with the same information:
```json
{
  "port": 12345,
  "token": "secret-auth-token", 
  "gameId": "your-game-id",
  "agentName": "gabs-v0.1.0",
  "host": "127.0.0.1"
}
```

**Note:** The bridge file is primarily for debugging. Always prioritize environment variables.

## Step 2: Acting as GABP Server

Your mod acts as a GABP server and needs to:

1. **Start a TCP server** on `127.0.0.1:GABP_SERVER_PORT`
2. **Wait for GABS to connect** (GABS acts as the client)
3. **Authenticate** using `GABP_TOKEN`
4. **Handle GABP protocol messages** (JSON-RPC format)
5. **Respond to tool calls** and **send events**

### Why Your Mod is the Server

- **Port Management**: GABS knows which ports are available and assigns unique ones
- **Concurrency**: Multiple game instances can run with different ports
- **Local Only**: Communication is always localhost (127.0.0.1) 
- **Lifecycle**: GABS launches your game, then connects when mod is ready

## Step 3: Exposing Your Features

GABP lets you expose three types of functionality:

### Tools
Functions that AI can call to do things in your game:
- `inventory/get` - Get player inventory
- `world/place_block` - Place a block in the world
- `player/teleport` - Move player to a location

### Resources
Files or data that AI can read:
- `world/save_data` - Current world state
- `config/settings` - Game configuration
- `logs/recent` - Recent game events

### Events
Real-time notifications about what's happening:
- `player/move` - Player changed position
- `world/block_placed` - Block was placed
- `server/player_joined` - Player joined the server

## Example Implementation

### C# (Unity/Harmony Mods)
```csharp
using System;
using System.IO;
using System.Net;
using System.Threading;
using Newtonsoft.Json;

public class GABPMod : Mod 
{
    private GABPServer server;
    
    public override void Start() 
    {
        // Read the bridge config
        var config = ReadBridgeConfig();
        
        // Start GABP server
        server = new GABPServer(config.Host, config.Port, config.Token);
        
        // Register your mod's capabilities
        server.RegisterTool("inventory/get", GetPlayerInventory);
        server.RegisterTool("world/place_block", PlaceBlock);
        server.RegisterEvent("player/move");
        
        // Start listening for GABS connections
        server.Listen();
        
        Console.WriteLine($"GABP server started on {config.Host}:{config.Port}");
    }
    
    private BridgeConfig ReadBridgeConfig()
    {
        // Method 1: Use environment variables (recommended)
        var gameId = Environment.GetEnvironmentVariable("GABS_GAME_ID");
        var portStr = Environment.GetEnvironmentVariable("GABP_SERVER_PORT");
        var token = Environment.GetEnvironmentVariable("GABP_TOKEN");
        
        if (!string.IsNullOrEmpty(portStr) && !string.IsNullOrEmpty(token) && 
            int.TryParse(portStr, out int port))
        {
            return new BridgeConfig
            {
                Host = "127.0.0.1", // Always localhost for GABP
                Port = port,        // Port to listen on as GABP server
                Token = token,      // Token for authenticating GABS connections
                GameId = gameId ?? "unknown"
            };
        }
        
        // Method 2: Use bridge file path (fallback for debugging)
        var bridgePath = Environment.GetEnvironmentVariable("GABS_BRIDGE_PATH");
        if (!string.IsNullOrEmpty(bridgePath) && File.Exists(bridgePath))
        {
            var json = File.ReadAllText(bridgePath);
            return JsonConvert.DeserializeObject<BridgeConfig>(json);
        }
        
        // Legacy fallback methods for backwards compatibility
        if (!string.IsNullOrEmpty(gameId))
        {
            var homeDir = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
            var configPath = Path.Combine(homeDir, ".gabs", gameId, "bridge.json");
            if (File.Exists(configPath))
            {
                var json = File.ReadAllText(configPath);
                return JsonConvert.DeserializeObject<BridgeConfig>(json);
            }
        }
        
        throw new Exception("GABP server config not found. Ensure GABS is running and game was started via GABS.");
    }
    
    private object GetPlayerInventory(object args)
    {
        // Return current player inventory
        return new { 
            items = Player.Current.Inventory.Items,
            slots = Player.Current.Inventory.SlotCount
        };
    }
    
    private object PlaceBlock(object args)
    {
        var request = JsonConvert.DeserializeObject<PlaceBlockRequest>(args.ToString());
        
        // Place the block in the world
        World.Current.SetBlock(request.X, request.Y, request.Z, request.BlockType);
        
        // Send event notification
        server.SendEvent("world/block_placed", new {
            x = request.X,
            y = request.Y, 
            z = request.Z,
            blockType = request.BlockType
        });
        
        return new { success = true };
    }
}

public class BridgeConfig
{
    public int Port { get; set; }      // Port to listen on as GABP server
    public string Token { get; set; }  // Token for authenticating GABS connections
    public string GameId { get; set; } // Game identifier
    public string Host { get; set; }   // Always "127.0.0.1" for GABP
}
```

### Java (Minecraft Mods)
```java
import java.io.*;
import java.net.*;
import com.google.gson.*;

public class GABPMod {
    private GABPServer server;
    
    public void onServerStart() {
        try {
            // Read bridge config
            BridgeConfig config = readBridgeConfig();
            
            // Start GABP server
            server = new GABPServer(config.host, config.port, config.token);
            
            // Register tools
            server.registerTool("inventory/get", this::getInventory);
            server.registerTool("world/set_block", this::setBlock);
            
            // Start listening
            server.start();
            
            System.out.println("GABP server started on " + config.host + ":" + config.port);
            
        } catch (Exception e) {
            e.printStackTrace();
        }
    }
    
    private BridgeConfig readBridgeConfig() throws IOException {
        // Method 1: Use environment variables (recommended)
        String gameId = System.getenv("GABS_GAME_ID");
        String portStr = System.getenv("GABP_SERVER_PORT");
        String token = System.getenv("GABP_TOKEN");
        
        if (portStr != null && token != null) {
            try {
                int port = Integer.parseInt(portStr);
                BridgeConfig config = new BridgeConfig();
                config.host = "127.0.0.1"; // Always localhost for GABP
                config.port = port;         // Port to listen on as GABP server
                config.token = token;       // Token for authenticating GABS connections
                config.gameId = gameId != null ? gameId : "unknown";
                return config;
            } catch (NumberFormatException e) {
                // Fall through to file-based methods
            }
        }
        
        // Method 2: Use bridge file path (fallback for debugging)
        String bridgePath = System.getenv("GABS_BRIDGE_PATH");
        if (bridgePath != null && !bridgePath.isEmpty()) {
            Path path = Paths.get(bridgePath);
            if (Files.exists(path)) {
                String json = Files.readString(path);
                return new Gson().fromJson(json, BridgeConfig.class);
            }
        }
        
        // Legacy fallback methods for backwards compatibility
        if (gameId != null && !gameId.isEmpty()) {
            String homeDir = System.getProperty("user.home");
            Path configPath = Paths.get(homeDir, ".gabs", gameId, "bridge.json");
            if (Files.exists(configPath)) {
                String json = Files.readString(configPath);
                return new Gson().fromJson(json, BridgeConfig.class);
            }
        }
        
        throw new IOException("GABP server config not found. Ensure GABS is running and game was started via GABS.");
    }
    
    private Object getInventory(Object args) {
        // Return player inventory data
        return Map.of(
            "items", getPlayerItems(),
            "hotbar", getHotbarItems()
        );
    }
    
    private Object setBlock(Object args) {
        SetBlockRequest request = new Gson().fromJson(args.toString(), SetBlockRequest.class);
        
        // Set the block in world
        world.setBlock(request.x, request.y, request.z, request.blockType);
        
        // Send event
        server.sendEvent("world/block_changed", Map.of(
            "x", request.x,
            "y", request.y,
            "z", request.z,
            "blockType", request.blockType
        ));
        
        return Map.of("success", true);
    }
}
```

### Python (Game Scripting)
```python
import json
import os
import socket
import threading
from pathlib import Path

class GABPMod:
    def __init__(self):
        self.server = None
        
    def start(self):
        # Read bridge config
        config = self.read_bridge_config()
        
        # Start GABP server
        self.server = GABPServer(config['host'], config['port'], config['token'])
        
        # Register capabilities
        self.server.register_tool('inventory/get', self.get_inventory)
        self.server.register_tool('player/teleport', self.teleport_player)
        
        # Start listening
        self.server.listen()
        print(f"GABP server started on {config['host']}:{config['port']}")
        
    def read_bridge_config(self):
        # Method 1: Use environment variables (recommended)
        game_id = os.environ.get('GABS_GAME_ID')
        port_str = os.environ.get('GABP_SERVER_PORT')
        token = os.environ.get('GABP_TOKEN')
        
        if port_str and token:
            try:
                port = int(port_str)
                return {
                    'host': '127.0.0.1',  # Always localhost for GABP
                    'port': port,         # Port to listen on as GABP server
                    'token': token,       # Token for authenticating GABS connections
                    'gameId': game_id or 'unknown'
                }
            except ValueError:
                # Fall through to file-based methods
                pass
        
        # Method 2: Use bridge file path (fallback for debugging)
        bridge_path = os.environ.get('GABS_BRIDGE_PATH')
        if bridge_path and os.path.exists(bridge_path):
            with open(bridge_path, 'r') as f:
                return json.load(f)
        
        # Legacy fallback methods for backwards compatibility
        if game_id:
            home_dir = Path.home()
            config_path = home_dir / '.gabs' / game_id / 'bridge.json'
            if config_path.exists():
                with open(config_path, 'r') as f:
                    return json.load(f)
        
        raise FileNotFoundError("GABP server config not found. Ensure GABS is running and game was started via GABS.")
            
    def get_inventory(self, args):
        """Return current player inventory"""
        return {
            'items': game.player.inventory.items,
            'gold': game.player.gold
        }
        
    def teleport_player(self, args):
        """Teleport player to specified location"""
        x, y, z = args['x'], args['y'], args['z']
        game.player.teleport(x, y, z)
        
        # Send event notification
        self.server.send_event('player/teleported', {
            'x': x, 'y': y, 'z': z,
            'player': game.player.name
        })
        
        return {'success': True}
```

## Protocol Details

GABP uses JSON-RPC 2.0 over TCP. Here are the main message types:

### Tool Call (from GABS to your mod)
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "inventory/get",
    "arguments": {}
  }
}
```

### Tool Response (from your mod to GABS)
```json
{
  "jsonrpc": "2.0", 
  "id": 1,
  "result": {
    "items": ["sword", "potion", "gold"],
    "count": 3
  }
}
```

### Event Notification (from your mod to GABS)
```json
{
  "jsonrpc": "2.0",
  "method": "notifications/event",
  "params": {
    "name": "player/move",
    "data": {
      "x": 10.5,
      "y": 64.0,
      "z": -15.2,
      "player": "Steve"
    }
  }
}
```

## Testing Your Implementation

1. **Test config reading**: Make sure your mod can read the bridge config from the correct location using the environment variables
2. **Test server startup**: Verify your mod starts a server on the right port
3. **Test with GABS**: Run `gabs games start your-game` and see if GABS can connect
4. **Test tools**: Use AI to call your tools and verify they work
5. **Test events**: Make sure events are sent when things happen in your game

### Environment Variables Set by GABS

When GABS starts your game, it sets these environment variables for GABP server configuration:

- `GABS_GAME_ID`: The game ID used by GABS (e.g., "minecraft", "rimworld")
- `GABP_SERVER_PORT`: Port number your mod should listen on as GABP server
- `GABP_TOKEN`: Authentication token for validating GABS connections
- `GABS_BRIDGE_PATH`: Path to bridge.json file (for debugging/compatibility only)

**Key Points:**
- Your mod listens on `127.0.0.1:GABP_SERVER_PORT` 
- GABS connects to your mod as a GABP client
- Use `GABP_TOKEN` to authenticate incoming connections from GABS

## Common Patterns

### Inventory Management
Most games need inventory tools:
```
inventory/get - Get current inventory
inventory/add - Add items
inventory/remove - Remove items
inventory/count - Count specific items
```

### World Interaction
For world-based games:
```
world/get_block - Get block at position
world/set_block - Place/change block
world/get_entities - List nearby entities
world/get_player_location - Get player position
```

### Server Management
For multiplayer games:
```
server/list_players - Show online players
server/kick_player - Remove a player
server/broadcast - Send message to all players
server/get_stats - Get server performance data
```

## Best Practices

1. **Keep it simple**: Start with basic tools and add more later
2. **Use clear names**: Tool names should be obvious (`inventory/get`, not `inv_data`)
3. **Handle errors**: Return helpful error messages when things go wrong
4. **Send events**: Let AI know when important things happen
5. **Document your tools**: Comment what each tool does and what arguments it takes
6. **Test thoroughly**: Make sure your mod works with and without GABS

## Getting Help

- Check the [GABP specification](https://github.com/pardeike/GABP) for full protocol details
- Look at example implementations in the GABS repository
- Join the community discussions for help with specific games
- Test your implementation with the GABS development tools
