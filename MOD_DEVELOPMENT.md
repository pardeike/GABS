# Mod Development Guide

This guide helps you add GABP support to your game mods so they can work with GABS.

## What is GABP?

[GABP](https://github.com/pardeike/GABP) (Game Agent Bridge Protocol) is a simple way for AI tools to talk to your game mods. Think of it like a translator between AI assistants and your game.

## Quick Overview

To work with GABS, your mod needs to:

1. **Read bridge config** when the game starts
2. **Act as GABP server** - listen for connections from GABS
3. **Expose your features** as tools, resources, and events

## Step 1: Reading Bridge Config

When GABS starts your game, it creates a file called `bridge.json` in your game's working directory. Your mod should read this file to know how to connect.

The config looks like this:
```json
{
  "port": 12345,
  "token": "secret-auth-token", 
  "gameId": "your-game-id",
  "agentName": "gabs-v0.1.0",
  "host": "127.0.0.1",
  "mode": "local"
}
```

## Step 2: Acting as GABP Server

Your mod needs to start a server that listens on the port specified in the bridge config. GABS will connect to this server to control your game.

### Basic Server Setup

Here's what your mod needs to do:

1. Start a TCP server on `host:port` from the bridge config
2. Use the `token` for authentication
3. Handle GABP protocol messages (JSON-RPC format)
4. Respond to tool calls and send events

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
        var configPath = Path.Combine(Application.dataPath, "bridge.json");
        var json = File.ReadAllText(configPath);
        return JsonConvert.DeserializeObject<BridgeConfig>(json);
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
    public int Port { get; set; }
    public string Token { get; set; }
    public string GameId { get; set; }
    public string AgentName { get; set; }
    public string Host { get; set; }
    public string Mode { get; set; }
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
        File configFile = new File("bridge.json");
        String json = Files.readString(configFile.toPath());
        return new Gson().fromJson(json, BridgeConfig.class);
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
        config_path = Path('bridge.json')
        with open(config_path, 'r') as f:
            return json.load(f)
            
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

1. **Test config reading**: Make sure your mod can read `bridge.json`
2. **Test server startup**: Verify your mod starts a server on the right port
3. **Test with GABS**: Run `gabs games start your-game` and see if GABS can connect
4. **Test tools**: Use AI to call your tools and verify they work
5. **Test events**: Make sure events are sent when things happen in your game

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
