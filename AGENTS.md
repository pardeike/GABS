# GABS Implementation Guide for AI Agents

## Your Mission

You are working with **GABS (Game Agent Bridge Server)**, which implements the GABP (Game Agent Bridge Protocol) as an MCP server. GABS enables AI tools to interact with games through GABP compliant modifications that act as a standardized bridge interface.

GABS acts as a translator between AI agents and GABP compliant game modifications, allowing AI systems to:
- Control and monitor games in real-time
- Debug and test game modifications 
- Analyze game state and player behavior
- Automate game testing and development workflows

## Architecture Overview

```
AI Agent ← MCP → GABS ← GABP → GABP Compliant Mod ← Game API → Game
```

- **AI Agent**: Your AI assistant (Claude, ChatGPT, custom tools)
- **MCP**: Model Context Protocol (standard AI-tool communication)
- **GABS**: Game Agent Bridge Server (this project)
- **GABP**: Game Agent Bridge Protocol (JSON-RPC style messaging)
- **GABP Compliant Mod**: Modification that implements GABP in the target game

### GABP Compliant Mod Types

GABS connects to game modifications that implement the GABP protocol. These mods can take different architectural approaches:

1. **Central Community Mods**: A single mod that searches for and exposes tools from all other installed mods, acting as a central hub for game functionality

2. **Individual Framework Mods**: Specific mods that use a GABP framework to expose their own unique functionality (crafting systems, building tools, etc.)

3. **General Game Control Mods**: Mods that make the entire game remotely controllable, providing access to core game mechanics rather than mod-specific features

4. **Combined Control Mods**: Mods that provide both game control and mod-specific functionality, allowing ultimate control over both the base game and modification features

This flexibility means you can have fine-grained control over specific mod functionality, broad control over the entire game, or any combination that suits your needs.

## Quick Start for AI Development

### 1. Understanding GABS from AI Perspective

GABS appears to your AI tool as an MCP server that provides access to GABP compliant game modifications with:

- **Tools**: Game actions you can execute (e.g., `inventory/get`, `world/place_block`)
- **Resources**: Game data you can read (e.g., `world/map.json`, `config/settings.txt`)
- **Events**: Real-time notifications from the game (e.g., `player/move`, `world/block_change`)

### 2. Basic Usage Pattern

```typescript
// AI agent connecting to GABS via MCP
const mcpClient = new MCPClient();
await mcpClient.connect("stdio://gabs server");

// Discover configured games and their status
const games = await mcpClient.callTool("games.list", {});
console.log("Available games:", games);

// Start a specific game
const result = await mcpClient.callTool("games.start", {
  gameId: "minecraft"  // or use Steam App ID like "294100"
});

// Check game status
const status = await mcpClient.callTool("games.status", {
  gameId: "minecraft"
});

// Once game mod connects, discover available game actions
const tools = await mcpClient.callTool("games.tools", {
  gameId: "minecraft"  
});
console.log("Game-specific tools:", tools);
```

### Enhanced Game Control

GABS now provides improved stopping capabilities through optional process name configuration:

```typescript
// For games with configured stopProcessName, stopping is more reliable
const stopResult = await mcpClient.callTool("games.stop", {
  gameId: "rimworld"  // Will stop actual RimWorld process, not just Steam launcher
});

// Force kill also works with process names
const killResult = await mcpClient.callTool("games.kill", {
  gameId: "minecraft"  // Will kill Java process if stopProcessName is "java"
});

// Check improved status information
const status = await mcpClient.callTool("games.status", {
  gameId: "rimworld"
});
// Status will indicate if process name stopping is available
```

### Process Name Configuration

When setting up games with better stopping control:
- **Steam/Epic Games**: Configure the actual game executable name (e.g., `"RimWorldWin64.exe"`)
- **Java Games**: Use `"java"` to stop Java processes
- **Unity Games**: Often `"GameName.exe"` on Windows
- **Custom**: Any process name that uniquely identifies your game

This addresses the common limitation where launcher-based games (Steam/Epic) could only have their launcher stopped, not the actual running game.

### 2. Starting GABS

Your AI tool connects to GABS which provides game integration:

```bash
# First: Configure your games once  
gabs games add minecraft
gabs games add rimworld

# Then: Start GABS as MCP server
gabs server
```

GABS automatically creates bridge configurations when games are started, allowing GABP compliant game mods to connect.

## MCP Integration Details

### Tools Exposed by GABS

GABS exposes game functionality as MCP tools. Common patterns:

#### Core Game Management Tools
- `games.list` - Discover configured games and their status
- `games.start` - Start a game using game ID or launch target
- `games.stop` - Gracefully stop a game (supports process name-based stopping for better control)
- `games.kill` - Force terminate a game (supports process name-based stopping for better control)  
- `games.stop` - Gracefully stop a running game
- `games.kill` - Force terminate a game
- `games.status` - Check detailed status of games
- `games.tools` - List available game-specific tools (from GABP compliant mods)

#### Game-Specific Tools (via GABP)
When a game's GABP compliant mod connects, additional tools become available:
- `inventory/get`, `inventory/set` - Player inventory management
- `world/get_block`, `world/place_block` - World manipulation
- `player/move`, `player/teleport` - Player actions
- `chat/send`, `chat/history` - Chat integration

*Note: Game-specific tools depend on what the GABP compliant mod exposes and may be prefixed with game ID for multi-game setups.*

### Resources Provided by GABS

GABS exposes game data as MCP resources:

- `config/*` - Game configuration files
- `world/*` - World/map data and schematics
- `logs/*` - Game logs and debug information
- `saves/*` - Save game files
- `mods/*` - Mod configuration and data

### Event Handling

GABS can stream real-time game events to your AI agent:

```typescript
// Subscribe to player events
await mcpClient.callTool("events/subscribe", {
  channels: ["player/move", "player/chat", "player/inventory_change"]
});

// Handle events as they arrive
mcpClient.onEvent("player/move", (event) => {
  const { playerId, position, timestamp } = event.payload;
  console.log(`${playerId} moved to ${position.x}, ${position.y}, ${position.z}`);
});

mcpClient.onEvent("player/chat", (event) => {
  const { playerId, message } = event.payload;
  // AI can respond to player chat
  if (message.includes("help")) {
    await mcpClient.callTool("chat/send", {
      message: "Hello! I'm an AI assistant. How can I help you?"
    });
  }
});
```

## Advanced AI Integration Patterns

### Game State Monitoring

```typescript
class GameStateMonitor {
  constructor(mcpClient) {
    this.client = mcpClient;
    this.gameState = {};
  }

  async startMonitoring() {
    // Monitor all configured games
    setInterval(async () => {
      const games = await this.client.callTool("games.list", {});
      this.gameState = games;
      
      // Check for games that have GABP tools available
      for (const game of games) {
        if (game.status === "running") {
          try {
            const tools = await this.client.callTool("games.tools", {
              gameId: game.id
            });
            console.log(`${game.id} has ${tools.length} GABP tools available`);
          } catch (error) {
            // Game mod hasn't connected yet
          }
        }
      }
    }, 5000);
  }

  async analyzeGameActivity(gameId) {
    const status = await this.client.callTool("games.status", { gameId });
    return status;
  }
}
```

### Automated Game Testing

```typescript
class GameTester {
  constructor(mcpClient) {
    this.client = mcpClient;
  }

  async testGameLaunch(gameId) {
    try {
      // Start the game
      const startResult = await this.client.callTool("games.start", { gameId });
      
      // Wait for game to initialize
      await new Promise(resolve => setTimeout(resolve, 5000));
      
      // Check if game is running
      const status = await this.client.callTool("games.status", { gameId });
      
      // Try to get GABP tools (indicates mod is connected)
      let tools = [];
      try {
        tools = await this.client.callTool("games.tools", { gameId });
      } catch (error) {
        // GABP mod hasn't connected yet
      }
      
      return {
        success: status.status === "running" || status.status === "launched",
        status: status.status,
        gabpTools: tools.length,
        pid: status.pid
      };
    } catch (error) {
      return { success: false, error: error.message };
    }
  }

  async runAutomatedTests() {
    const games = await this.client.callTool("games.list", {});
    const results = [];
    
    for (const game of games) {
      const result = await this.testGameLaunch(game.id);
      results.push({ gameId: game.id, ...result });
    }

    return results;
  }
}
```

### 3. AI-Assisted Debugging

```typescript
class GameDebugger {
  constructor(mcpClient) {
    this.client = mcpClient;
    this.issues = [];
  }

  async detectAnomalies() {
    // Monitor for unusual patterns
    this.client.onEvent("*", (event) => {
      if (this.isAnomalous(event)) {
        this.issues.push({
          type: "anomaly",
          event: event,
          timestamp: Date.now(),
          context: this.getGameContext()
        });
      }
    });
  }

  async diagnoseIssue(issue) {
    // Read relevant game logs and state
    const logs = await this.client.readResource("logs/latest.log");
    const gameState = await this.client.callTool("state/get", {});
    
    // AI analysis to identify root cause
    const diagnosis = await this.analyzeWithAI({
      issue: issue,
      logs: logs,
      gameState: gameState
    });

    return diagnosis;
  }

  async suggestFixes(diagnosis) {
    // AI-generated suggestions for fixing issues
    return await this.generateFixSuggestions(diagnosis);
  }
}
```

## GABP Protocol Details for AI

### Message Format

GABS translates between MCP and GABP protocols. Understanding GABP helps debug issues:

```json
// GABP Request (what GABS sends to game mod)
{
  "v": "gabp/1",
  "id": "550e8400-e29b-41d4-a716-446655440000", 
  "type": "request",
  "method": "inventory/get",
  "params": { "playerId": "steve" }
}

// GABP Response (what game mod sends back)
{
  "v": "gabp/1",
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "type": "response", 
  "result": {
    "items": [
      { "id": "minecraft:wood", "count": 64 },
      { "id": "minecraft:stone", "count": 32 }
    ]
  }
}

// GABP Event (real-time notifications)
{
  "v": "gabp/1",
  "id": "550e8400-e29b-41d4-a716-446655440001",
  "type": "event",
  "channel": "player/move", 
  "seq": 42,
  "payload": {
    "playerId": "steve",
    "position": { "x": 100, "y": 64, "z": 200 },
    "timestamp": 1609459200000
  }
}
```

### Error Handling

GABP uses JSON-RPC style error codes:

- `-32600`: Invalid Request  
- `-32601`: Method Not Found
- `-32602`: Invalid Params
- `-32603`: Internal Error
- `-32000` to `-32099`: Server Errors

```typescript
// Handle GABP errors in your AI code
try {
  const result = await mcpClient.callTool("invalid/method", {});
} catch (error) {
  if (error.code === -32601) {
    console.log("Method not supported by this game mod");
    // Graceful fallback or user notification
  }
}
```

## Multi-Game Tool Management

### The Challenge with Multiple Games

When you have multiple games running simultaneously (e.g., Minecraft and RimWorld), each with their own GABP-compliant mods, GABS faces a critical challenge: **tool name conflicts**.

**Problem Example:**
- Minecraft mod exposes: `inventory/get`, `world/place_block`, `player/teleport`  
- RimWorld mod exposes: `inventory/get`, `crafting/build`, `player/teleport`

Without proper handling, both games would register `inventory/get`, causing:
- Tool conflicts (last registered wins)
- AI confusion (which game's inventory?)
- No way to specify target game
- Unpredictable behavior

### GABS Solution: Game-Prefixed Tool Names

GABS solves this by **automatically prefixing all mirrored tools with the game ID**:

```typescript
// Before (confusing):
inventory/get       // Which game?
player/teleport     // Minecraft or RimWorld?

// After (crystal clear):
minecraft.inventory/get     // Obviously Minecraft's inventory
rimworld.inventory/get      // Obviously RimWorld's inventory  
minecraft.player/teleport   // Minecraft teleportation
rimworld.player/teleport    // RimWorld colonist movement
```

### AI Usage Patterns

#### 1. Discovering Available Games and Tools

```typescript
// First, see what games are configured and their status
const games = await mcpClient.callTool("games.list", {});
console.log("Available games:", games);

// Check individual game status
const minecraftStatus = await mcpClient.callTool("games.status", {
  gameId: "minecraft"
});

// See what GABP tools a running game provides
if (minecraftStatus.status === "running") {
  try {
    const minecraftTools = await mcpClient.callTool("games.tools", {
      gameId: "minecraft"
    });
    console.log("Minecraft GABP tools:", minecraftTools);
  } catch (error) {
    console.log("Minecraft mod not yet connected");
  }
}
```

#### 2. Using Game-Specific Tools

```typescript
// Start games first
await mcpClient.callTool("games.start", { gameId: "minecraft" });
await mcpClient.callTool("games.start", { gameId: "rimworld" });

// Once GABP mods connect, you can use their exposed tools
// (Tool names depend on what each mod implements)
try {
  const inventory = await mcpClient.callTool("inventory/get", {
    playerId: "steve"
  });
  console.log("Minecraft inventory:", inventory);
} catch (error) {
  console.log("Minecraft inventory tool not available");
}
```

#### 3. Multi-Game Coordination

```typescript
// Start multiple games
const games = ["minecraft", "rimworld"];
const startResults = await Promise.allSettled(
  games.map(gameId => mcpClient.callTool("games.start", { gameId }))
);

// Check which games are running
const statusResults = await Promise.all(
  games.map(gameId => mcpClient.callTool("games.status", { gameId }))
);

console.log("Running games:", 
  statusResults.filter(s => s.status === "running" || s.status === "launched")
);
```

### Advanced Multi-Game Scenarios

#### Cross-Game Resource Management

```typescript
// AI can manage resources across multiple games
const resources = await Promise.all([
  mcpClient.readResource("gab://minecraft/events/logs"),
  mcpClient.readResource("gab://rimworld/events/logs")
]);

// Compare activity levels
const minecraftEvents = JSON.parse(resources[0]);
const rimworldEvents = JSON.parse(resources[1]);

if (minecraftEvents.length > rimworldEvents.length) {
  console.log("Minecraft is more active today");
} else {
  console.log("RimWorld colony needs attention");
}
```

#### Automated Multi-Game Testing

```typescript
class MultiGameTester {
  async testInventorySystems() {
    const games = ["minecraft", "rimworld"];
    const results = {};
    
    for (const gameId of games) {
      try {
        // Test inventory functionality in each game
        const inventory = await this.mcpClient.callTool(`${gameId}.inventory/get`, {
          playerId: "test-player"
        });
        
        results[gameId] = {
          success: true,
          itemCount: inventory.items?.length || 0
        };
      } catch (error) {
        results[gameId] = {
          success: false,
          error: error.message
        };
      }
    }
    
    return results;
  }
}
```

### Implementation Details for AI Developers

#### Tool Discovery Strategy
1. **Use `games.list`** to see configured games and their status
2. **Use `games.tools`** to discover what tools each game provides  
3. **Look for game prefixes** (e.g., `minecraft.`, `rimworld.`) to identify game-specific tools
4. **Filter tools by game** when you need to work with specific games

#### Error Handling Best Practices
```typescript
async function callGameTool(gameId, toolName, args) {
  const fullToolName = `${gameId}.${toolName}`;
  
  try {
    return await mcpClient.callTool(fullToolName, args);
  } catch (error) {
    if (error.code === -32601) {
      // Tool not found - maybe game not running or mod not installed
      const gameStatus = await mcpClient.callTool("games.status", { gameId });
      throw new Error(`${toolName} not available for ${gameId}. Game status: ${gameStatus}`);
    }
    throw error;
  }
}
```

#### Resource Naming Convention
Resources are also game-prefixed to avoid conflicts:
- `gab://minecraft/events/logs` - Minecraft event logs
- `gab://rimworld/world/map` - RimWorld colony map
- `gab://minecraft/config/settings` - Minecraft server settings

### Migration from Single-Game Setups

If you have existing AI code that assumes single-game operation:

**Before (single game):**
```typescript
const inventory = await mcpClient.callTool("inventory/get", { playerId: "steve" });
```

**After (multi-game aware):**
```typescript
// Option 1: Explicit game targeting
const inventory = await mcpClient.callTool("minecraft.inventory/get", { playerId: "steve" });

// Option 2: Dynamic game selection
const primaryGame = "minecraft"; // from config or user preference
const inventory = await mcpClient.callTool(`${primaryGame}.inventory/get`, { playerId: "steve" });

// Option 3: Try all games until one succeeds
for (const gameId of ["minecraft", "rimworld"]) {
  try {
    const inventory = await mcpClient.callTool(`${gameId}.inventory/get`, { playerId: "steve" });
    if (inventory) break;
  } catch (error) {
    continue; // Try next game
  }
}
```

### Benefits for AI Agents

1. **Crystal Clear Intent**: `minecraft.inventory/get` vs `rimworld.inventory/get` - no ambiguity
2. **Parallel Game Control**: AI can manage multiple games simultaneously without conflicts  
3. **Robust Error Handling**: AI knows exactly which game failed and why
4. **Scalable Architecture**: Add more games without tool name conflicts
5. **Better User Experience**: AI can tell users exactly which game it's interacting with

## Game Mod Integration

### For AI Agents Helping Mod Development

If you're an AI assistant helping someone develop a GABP compliant game mod:

#### 1. Reading Bridge Configuration

```csharp
// Example: C# mod reading GABS bridge config
public class GABPConfig {
    public int Port { get; set; }
    public string Token { get; set; } 
    public string LaunchId { get; set; }
}

public static GABPConfig ReadBridgeConfig(string gameId) {
    var configPath = GetConfigPath(gameId); // Platform-specific path
    var bridgeFile = Path.Combine(configPath, "bridge.json");
    
    if (!File.Exists(bridgeFile)) {
        throw new Exception("Bridge config not found. Is GABS running?");
    }
    
    var json = File.ReadAllText(bridgeFile);
    return JsonConvert.DeserializeObject<GABPConfig>(json);
}
```

#### 2. GABP Client Implementation

```csharp
public class GABPClient {
    private TcpClient tcpClient;
    private NetworkStream stream;
    
    public async Task Connect(int port, string token) {
        tcpClient = new TcpClient("127.0.0.1", port);
        stream = tcpClient.GetStream();
        
        // Send handshake
        await SendRequest("session/hello", new {
            token = token,
            bridgeVersion = "1.0.0",
            platform = GetPlatform(),
            launchId = Environment.GetEnvironmentVariable("GABS_LAUNCH_ID")
        });
    }
    
    public async Task RegisterTool(string name, Func<object, object> handler) {
        // Register tool implementation that GABS can call
    }
    
    public async Task EmitEvent(string channel, object payload) {
        // Send event to GABS for forwarding to AI agents
    }
}
```

#### 3. Tool Implementation Pattern

```csharp
public class MinecraftMod : GABPMod {
    protected override void RegisterTools() {
        RegisterTool("inventory/get", GetInventory);
        RegisterTool("inventory/set", SetInventory);
        RegisterTool("world/get_block", GetBlock);
        RegisterTool("world/place_block", PlaceBlock);
        RegisterTool("player/teleport", TeleportPlayer);
    }
    
    private object GetInventory(object parameters) {
        var param = JsonConvert.DeserializeObject<dynamic>(parameters);
        string playerId = param.playerId;
        
        var player = GetPlayer(playerId);
        return new {
            items = player.Inventory.Items.Select(item => new {
                id = item.ItemId,
                count = item.Count,
                metadata = item.Metadata
            })
        };
    }
    
    private object PlaceBlock(object parameters) {
        var param = JsonConvert.DeserializeObject<dynamic>(parameters);
        
        try {
            var world = GetWorld();
            world.SetBlock(param.x, param.y, param.z, param.blockType);
            
            // Emit event for AI agents to receive
            EmitEvent("world/block_change", new {
                position = new { x = param.x, y = param.y, z = param.z },
                blockType = param.blockType,
                timestamp = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()
            });
            
            return new { success = true };
        } catch (Exception ex) {
            throw new GABPException(-32603, $"Failed to place block: {ex.Message}");
        }
    }
}
```

## Configuration and Deployment

### Platform-Specific Config Locations

GABS creates configuration in the user's home directory:

- **All platforms**: `~/.gabs/{gameId}/bridge.json`

### Environment Variables

- `GABS_LAUNCH_ID`: Unique session identifier
- `GABS_LOG_LEVEL`: Logging verbosity (trace, debug, info, warn, error)
- `GABS_CONFIG_DIR`: Override default config directory

### Security Considerations

- All connections are loopback-only (127.0.0.1)
- Token-based authentication prevents unauthorized access
- GABS never exposes ports to external networks
- GABP compliant game mods should validate all tool parameters

## Debugging and Troubleshooting

### Common Issues

1. **"Bridge config not found"**
   - Ensure GABS is running with `gabs status --gameId <id>`
   - Check config directory permissions

2. **"Connection refused"**
   - Verify port number in bridge.json
   - Check firewall/antivirus blocking local connections

3. **"Invalid token"**
   - Ensure game mod reads token from bridge.json correctly
   - Check token string matches exactly

4. **"Method not found"** 
   - GABP compliant game mod hasn't implemented the requested tool
   - Check mod's tool registration code

### Debug Logging

Enable detailed logging in GABS:

```bash
gabs run --gameId mygame --target "/path/to/game" --log-level debug
```

Monitor GABP messages in your GABP compliant game mod:

```csharp
public class DebugGABPClient : GABPClient {
    protected override async Task SendMessage(object message) {
        Console.WriteLine($"SEND: {JsonConvert.SerializeObject(message, Formatting.Indented)}");
        await base.SendMessage(message);
    }
    
    protected override async Task<object> ReceiveMessage() {
        var message = await base.ReceiveMessage();
        Console.WriteLine($"RECV: {JsonConvert.SerializeObject(message, Formatting.Indented)}");
        return message;
    }
}
```

## Best Practices for AI Agents

### 1. Graceful Degradation

```typescript
class RobustGameAgent {
  async executeAction(action, params, fallback = null) {
    try {
      return await this.mcpClient.callTool(action, params);
    } catch (error) {
      if (error.code === -32601 && fallback) {
        console.log(`${action} not available, trying fallback`);
        return await this.executeAction(fallback.action, fallback.params);
      }
      throw error;
    }
  }
}
```

### 2. State Synchronization

```typescript
class GameStateSync {
  constructor(mcpClient) {
    this.client = mcpClient;
    this.localState = {};
    this.syncInterval = null;
  }
  
  startSync(intervalMs = 1000) {
    this.syncInterval = setInterval(async () => {
      try {
        const remoteState = await this.client.callTool("state/get", {});
        this.mergeState(remoteState);
      } catch (error) {
        console.error("State sync failed:", error);
      }
    }, intervalMs);
  }
  
  mergeState(remoteState) {
    // Intelligent merging of local and remote state
    this.localState = { ...this.localState, ...remoteState };
    this.onStateChange(this.localState);
  }
}
```

### 3. Event Processing

```typescript
class GameEventProcessor {
  constructor(mcpClient) {
    this.client = mcpClient;
    this.eventBuffer = [];
    this.maxBufferSize = 1000;
  }
  
  async subscribeToEvents(channels) {
    await this.client.callTool("events/subscribe", { channels });
    
    this.client.onEvent("*", (event) => {
      this.bufferEvent(event);
      this.processEvent(event);
    });
  }
  
  processEvent(event) {
    // Real-time event processing
    switch (event.channel) {
      case "player/move":
        this.handlePlayerMove(event.payload);
        break;
      case "world/block_change":
        this.handleWorldChange(event.payload);
        break;
      default:
        this.handleGenericEvent(event);
    }
  }
  
  bufferEvent(event) {
    this.eventBuffer.push(event);
    if (this.eventBuffer.length > this.maxBufferSize) {
      this.eventBuffer.shift(); // Remove oldest event
    }
  }
  
  getRecentEvents(channel, limit = 10) {
    return this.eventBuffer
      .filter(e => e.channel === channel)
      .slice(-limit);
  }
}
```

## Performance Optimization

### Connection Management

```typescript
class OptimizedMCPClient {
  constructor() {
    this.connectionPool = new Map();
    this.requestQueue = [];
    this.processing = false;
  }
  
  async callTool(name, params) {
    return new Promise((resolve, reject) => {
      this.requestQueue.push({ name, params, resolve, reject });
      this.processQueue();
    });
  }
  
  async processQueue() {
    if (this.processing || this.requestQueue.length === 0) return;
    
    this.processing = true;
    
    // Batch multiple requests for efficiency
    const batch = this.requestQueue.splice(0, 10);
    const results = await this.executeBatch(batch);
    
    batch.forEach((req, i) => {
      if (results[i].error) {
        req.reject(results[i].error);
      } else {
        req.resolve(results[i].result);
      }
    });
    
    this.processing = false;
    
    // Process remaining queue
    if (this.requestQueue.length > 0) {
      setImmediate(() => this.processQueue());
    }
  }
}
```

### Resource Caching

```typescript
class ResourceCache {
  constructor(mcpClient, ttlMs = 30000) {
    this.client = mcpClient;
    this.cache = new Map();
    this.ttl = ttlMs;
  }
  
  async readResource(uri) {
    const cacheKey = uri;
    const cached = this.cache.get(cacheKey);
    
    if (cached && Date.now() - cached.timestamp < this.ttl) {
      return cached.data;
    }
    
    const data = await this.client.readResource(uri);
    this.cache.set(cacheKey, {
      data: data,
      timestamp: Date.now()
    });
    
    return data;
  }
  
  invalidate(uriPattern) {
    for (const [key] of this.cache) {
      if (key.match(uriPattern)) {
        this.cache.delete(key);
      }
    }
  }
}
```

## Testing Your Integration

### Unit Testing

```typescript
describe('GameAgent', () => {
  let mockMCPClient;
  let gameAgent;
  
  beforeEach(() => {
    mockMCPClient = {
      callTool: jest.fn(),
      readResource: jest.fn(),
      onEvent: jest.fn()
    };
    gameAgent = new GameAgent(mockMCPClient);
  });
  
  test('should handle inventory requests', async () => {
    mockMCPClient.callTool.mockResolvedValue({
      items: [{ id: 'wood', count: 64 }]
    });
    
    const result = await gameAgent.getInventory('player1');
    
    expect(mockMCPClient.callTool).toHaveBeenCalledWith('inventory/get', {
      playerId: 'player1'
    });
    expect(result.items).toHaveLength(1);
  });
});
```

### Integration Testing

```typescript
class GABSIntegrationTest {
  async runFullTest() {
    // Start GABS with test game
    const gabs = await this.startGABS({
      gameId: 'test-game',
      target: './test/mock-game.exe'
    });
    
    // Connect MCP client  
    const client = new MCPClient();
    await client.connect('stdio://gabs');
    
    // Test tool discovery
    const tools = await client.listTools();
    assert(tools.length > 0, 'No tools available');
    
    // Test tool execution
    const result = await client.callTool('test/ping', {});
    assert(result.message === 'pong', 'Ping test failed');
    
    // Test event handling
    const events = [];
    client.onEvent('test/event', e => events.push(e));
    
    await client.callTool('events/subscribe', { channels: ['test/event'] });
    await client.callTool('test/emit_event', {});
    
    await new Promise(resolve => setTimeout(resolve, 100));
    assert(events.length === 1, 'Event not received');
    
    await gabs.stop();
  }
}
```

## Conclusion

GABS provides a powerful bridge between AI agents and games through the GABP protocol. By implementing the patterns and practices outlined in this guide, you can build robust AI systems that effectively interact with game modifications.

Key takeaways:
- Use MCP tools to execute game actions
- Read game state via MCP resources  
- Subscribe to real-time events for monitoring
- Implement proper error handling and fallbacks
- Cache resources and batch requests for performance
- Follow security best practices for token handling

For more details, see:
- [GABP Protocol Specification](https://github.com/pardeike/GABP)
- [MCP Protocol Documentation](https://spec.modelcontextprotocol.io/)
- [GABS Source Code](https://github.com/pardeike/GABS)