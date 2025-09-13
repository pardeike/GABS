# GABS Implementation Guide for AI Agents

## Your Mission

You are working with **GABS (Game Agent Bridge Server)**, which implements the GABP (Game Agent Bridge Protocol) as an MCP server. GABS enables AI tools to interact with games through a standardized bridge interface.

GABS acts as a translator between AI agents and game modifications, allowing AI systems to:
- 🎮 Control and monitor games in real-time
- 🔧 Debug and test game modifications 
- 📊 Analyze game state and player behavior
- 🤖 Automate game testing and development workflows

## Architecture Overview

```
AI Agent ← MCP → GABS ← GABP → Game Mod ← Game API → Game
```

- **AI Agent**: Your AI assistant (Claude, ChatGPT, custom tools)
- **MCP**: Model Context Protocol (standard AI-tool communication)
- **GABS**: Game Agent Bridge Server (this project)
- **GABP**: Game Agent Bridge Protocol (JSON-RPC style messaging)
- **Game Mod**: Modification that implements GABP in the target game

## Quick Start for AI Development

### 1. Understanding GABS from AI Perspective

GABS appears to your AI tool as an MCP server that provides:

- **Tools**: Game actions you can execute (e.g., `inventory/get`, `world/place_block`)
- **Resources**: Game data you can read (e.g., `world/map.json`, `config/settings.txt`)
- **Events**: Real-time notifications from the game (e.g., `player/move`, `world/block_change`)

### 2. Basic Usage Pattern

```typescript
// AI agent connecting to GABS via MCP
const mcpClient = new MCPClient();
await mcpClient.connect("stdio://gabs");

// Discover available game tools
const tools = await mcpClient.listTools();
console.log("Available game actions:", tools.map(t => t.name));

// Execute a game action  
const result = await mcpClient.callTool("inventory/get", {
  playerId: "steve"
});

// Read game data
const worldData = await mcpClient.readResource("world/schematic.json");

// Subscribe to game events (if supported)
mcpClient.onEvent("player/move", (event) => {
  console.log(`Player moved to: ${event.payload.position}`);
});
```

### 3. Starting GABS

Your AI tool can launch GABS to start game integration:

```bash
# Start game with GABS bridge
gabs run --gameId minecraft --launch DirectPath --target "/path/to/minecraft.exe"

# Or attach to already running game
gabs attach --gameId minecraft
```

## MCP Integration Details

### Tools Exposed by GABS

GABS exposes game functionality as MCP tools. Common patterns:

#### Core GABP Tools
- `tools/list` - Discover available game actions
- `tools/call` - Execute specific game actions
- `events/subscribe` - Subscribe to game events
- `events/unsubscribe` - Unsubscribe from events
- `resources/list` - List available game resources
- `resources/read` - Read specific game resource

#### Bridge Management Tools  
- `bridge/status` - Check bridge connection status
- `bridge/restart` - Restart the game process
- `bridge/refresh` - Refresh tool/resource discovery

#### Game-Specific Tools
These depend on what the game mod exposes:
- `inventory/get`, `inventory/set` - Player inventory management
- `world/get_block`, `world/place_block` - World manipulation
- `player/move`, `player/teleport` - Player actions
- `chat/send`, `chat/history` - Chat integration

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

### 1. Game State Monitoring

```typescript
class GameStateMonitor {
  constructor(mcpClient) {
    this.client = mcpClient;
    this.gameState = {};
  }

  async startMonitoring() {
    // Subscribe to all relevant events
    await this.client.callTool("events/subscribe", {
      channels: ["player/*", "world/*", "game/*"]
    });

    // Periodic state snapshots
    setInterval(async () => {
      const state = await this.client.callTool("state/get", {});
      this.gameState = { ...this.gameState, ...state };
    }, 5000);
  }

  async analyzePlayerBehavior(playerId) {
    // AI analysis of player patterns
    const recentMoves = this.getRecentEvents("player/move", playerId);
    const analysis = await this.runAIAnalysis(recentMoves);
    return analysis;
  }
}
```

### 2. Automated Game Testing

```typescript
class GameTester {
  constructor(mcpClient) {
    this.client = mcpClient;
  }

  async testCraftingSystem() {
    try {
      // Get initial inventory
      const startInventory = await this.client.callTool("inventory/get", {});
      
      // Attempt crafting
      const craftResult = await this.client.callTool("crafting/craft", {
        recipe: "wooden_sword",
        quantity: 1
      });

      // Verify results
      const endInventory = await this.client.callTool("inventory/get", {});
      
      return this.validateCraftingResults(startInventory, endInventory, craftResult);
    } catch (error) {
      return { success: false, error: error.message };
    }
  }

  async runAutomatedTests() {
    const tests = [
      () => this.testCraftingSystem(),
      () => this.testMovementSystem(), 
      () => this.testInventoryManagement(),
      () => this.testWorldInteraction()
    ];

    const results = [];
    for (const test of tests) {
      const result = await test();
      results.push(result);
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

## Game Mod Integration

### For AI Agents Helping Mod Development

If you're an AI assistant helping someone develop a game mod:

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

GABS creates configuration in standard locations:

- **Windows**: `%APPDATA%\GAB\{gameId}\bridge.json`
- **macOS**: `~/Library/Application Support/GAB/{gameId}/bridge.json`
- **Linux**: `~/.local/state/gab/{gameId}/bridge.json`

### Environment Variables

- `GABS_LAUNCH_ID`: Unique session identifier
- `GABS_LOG_LEVEL`: Logging verbosity (trace, debug, info, warn, error)
- `GABS_CONFIG_DIR`: Override default config directory

### Security Considerations

- All connections are loopback-only (127.0.0.1)
- Token-based authentication prevents unauthorized access
- GABS never exposes ports to external networks
- Game mods should validate all tool parameters

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
   - Game mod hasn't implemented the requested tool
   - Check mod's tool registration code

### Debug Logging

Enable detailed logging in GABS:

```bash
gabs run --gameId mygame --target "/path/to/game" --log-level debug
```

Monitor GABP messages in your game mod:

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