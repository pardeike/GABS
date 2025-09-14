# Dynamic Tool Discovery Guide for AI Agents

## Understanding GABS Dynamic Tool System

GABS uses a **progressive tool disclosure** system that helps AI agents handle the dynamic expansion of available tools as games connect. This guide explains how AI agents can effectively work with GABS's growing tool ecosystem.

> **Related guides:** [AI Integration Guide](INTEGRATION.md) for connecting AI agents, [Configuration Guide](CONFIGURATION.md) for game setup, [AI Dynamic Tools FAQ](AI_DYNAMIC_TOOLS_FAQ.md) for common questions.

## The Progressive Tool Discovery Pattern

### Phase 1: Initial State (Core Game Management)
When GABS starts, AI agents see only core game management tools:

```
Available Tools (5):
- games.list    - List configured games
- games.start   - Start a game  
- games.stop    - Stop a game gracefully
- games.kill    - Force terminate a game
- games.status  - Check game status
- games.tools   - List game-specific tools (key discovery tool!)
```

### Phase 2: Game Connection (Dynamic Expansion)
After starting a game with GABP mods, tools expand dramatically:

```
Available Tools (15+):
Core Tools:
- games.list, games.start, games.stop, games.kill, games.status, games.tools

Minecraft-Specific Tools (after minecraft starts):
- minecraft.inventory.get     - Get player inventory
- minecraft.inventory.set     - Modify player inventory  
- minecraft.world.get_block   - Check block at position
- minecraft.world.place_block - Place block in world
- minecraft.player.teleport   - Teleport player
- minecraft.chat.send         - Send chat message
- minecraft.time.set          - Set world time
- minecraft.weather.set       - Control weather

RimWorld-Specific Tools (if rimworld also starts):
- rimworld.inventory.get      - Get colonist inventory
- rimworld.crafting.build     - Build items/structures
- rimworld.colonist.command   - Give colonist orders
- rimworld.research.progress  - Check research status
```

## AI Discovery Strategies

### 1. The `games.tools` Discovery Pattern (Recommended)

**Best Practice**: Always use `games.tools` to discover available functionality before attempting game-specific actions.

```javascript
// AI Discovery Workflow
async function discoverGameCapabilities() {
  // 1. See what games are available
  const games = await mcp.callTool("games.list", {});
  
  // 2. Check what games are running  
  const status = await mcp.callTool("games.status", {});
  
  // 3. Discover tools for each running game
  const allTools = await mcp.callTool("games.tools", {});
  
  // 4. Get specific tools for game of interest
  const minecraftTools = await mcp.callTool("games.tools", {"gameId": "minecraft"});
  
  return { games, status, allTools, minecraftTools };
}
```

### 2. Progressive Enhancement Pattern

AI agents should treat tool discovery as a progressive enhancement:

```
Phase 1: Use core tools to start games
"Start the Minecraft server" → games.start {"gameId": "minecraft"}

Phase 2: Discover what's now possible  
"What can I do with Minecraft now?" → games.tools {"gameId": "minecraft"}

Phase 3: Use discovered tools
"Get Steve's inventory" → minecraft.inventory.get {"playerId": "steve"}
```

### 3. Context-Aware Tool Usage

Tools are namespaced by game ID to prevent conflicts:

```
✅ Clear Intent:
- minecraft.inventory.get {"playerId": "steve"}      # Steve's Minecraft inventory
- rimworld.inventory.get {"colonistId": "alice"}     # Alice's RimWorld inventory

❌ Ambiguous (avoided by design):  
- inventory.get {"playerId": "steve"}                # Which game?
```

## Real-World AI Interaction Examples

### Example 1: Starting Fresh
```
User: "Help me manage my games"
AI: Let me see what games you have configured...
    → games.list
    
AI: I see you have minecraft and rimworld configured. Neither is running.
    Would you like me to start one of them?

User: "Start Minecraft"  
AI: Starting Minecraft...
    → games.start {"gameId": "minecraft"}
    
AI: Minecraft started! Let me see what I can help you with...
    → games.tools {"gameId": "minecraft"}
    
AI: Great! Now I can help you with:
    - Managing your inventory (minecraft.inventory.get/set)
    - Building in your world (minecraft.world.place_block) 
    - Teleporting around (minecraft.player.teleport)
    - Controlling time and weather
    What would you like to do?
```

### Example 2: Multi-Game Management
```
User: "I want to run both games and manage them"
AI: I'll start both games and show you the expanded capabilities...
    → games.start {"gameId": "minecraft"}
    → games.start {"gameId": "rimworld"}  
    → games.tools {}
    
AI: Perfect! Now I can help you with:
    
    Minecraft (8 tools available):
    - Inventory management
    - World building  
    - Player control
    
    RimWorld (6 tools available):
    - Colony management
    - Crafting systems
    - Research progress
    
    The tools are clearly separated by game name, so I can help
    you manage both simultaneously without confusion.
```

### Example 3: Discovery-Driven Assistance
```
User: "What's possible with my Minecraft server?"
AI: Let me check what your Minecraft mod exposes...
    → games.tools {"gameId": "minecraft"}
    
AI: Your Minecraft setup provides these capabilities:
    - Inventory System: Get/modify player inventories
    - World Interaction: Place/check blocks, teleport players  
    - Server Control: Manage time, weather, send chat messages
    
    This suggests you have a comprehensive admin/creative mod installed.
    What would you like me to help you with?

User: "Give me a diamond sword"
AI: I'll add a diamond sword to your inventory...
    → minecraft.inventory.set {"playerId": "steve", "item": "diamond_sword", "count": 1}
```

## Technical Implementation for AI Developers

### 1. Tool List Caching with Refresh
```javascript
class GABSToolManager {
  constructor(mcpClient) {
    this.mcp = mcpClient;
    this.toolCache = new Map();
    this.lastRefresh = 0;
    this.refreshInterval = 30000; // 30 seconds
  }
  
  async getAvailableTools(gameId = null) {
    if (this.shouldRefresh()) {
      await this.refreshToolCache();
    }
    
    if (gameId) {
      return this.toolCache.get(gameId) || [];
    }
    
    return Array.from(this.toolCache.values()).flat();
  }
  
  async refreshToolCache() {
    const tools = await this.mcp.callTool("games.tools", {});
    // Parse and cache by game...
    this.lastRefresh = Date.now();
  }
}
```

### 2. Smart Tool Discovery
```javascript
async function intelligentGameManagement(request) {
  // 1. Parse user intent
  const intent = parseUserRequest(request);
  
  // 2. Check if we need to start games first
  if (intent.requiresGame && !await isGameRunning(intent.gameId)) {
    await startGame(intent.gameId);
    
    // 3. Wait a moment for GABP connection
    await sleep(2000);
    
    // 4. Refresh tool knowledge
    await refreshAvailableTools();
  }
  
  // 5. Execute with discovered tools
  return await executeWithDiscoveredTools(intent);
}
```

## Best Practices for AI Agents

### ✅ Do:
1. **Always use `games.tools` for discovery** before attempting game-specific actions
2. **Cache tool lists** but refresh periodically or after game state changes  
3. **Handle tool expansion gracefully** - treat new tools as opportunities
4. **Use game-prefixed tool names** explicitly to avoid ambiguity
5. **Group tools by game** in user interfaces for clarity

### ❌ Don't:
1. **Don't assume tools exist** without checking via `games.tools`
2. **Don't cache tools indefinitely** - they change as games start/stop
3. **Don't try to use generic tool names** like `inventory.get` 
4. **Don't overwhelm users** with massive tool lists - group and filter intelligently

## GABS Design Advantages for AI

The GABS architecture specifically helps AI agents handle dynamic tools:

1. **Clear Namespacing**: `minecraft.inventory.get` vs `rimworld.inventory.get` eliminates ambiguity
2. **Discovery Tool**: `games.tools` provides structured tool exploration  
3. **Gradual Disclosure**: Tools appear as games connect, not all at once
4. **Status Awareness**: AI can check game state before attempting tool use
5. **Fallback Gracefully**: Core game management always available

## Conclusion

AI agents working with GABS will handle dynamic tool expansion effectively because:

1. **The discovery pattern is predictable** - start with `games.tools`
2. **Tool names are unambiguous** - game prefixes prevent conflicts  
3. **Expansion is progressive** - tools appear as capabilities are needed
4. **The core remains stable** - basic game management always works

The key insight is that AI agents should **embrace the discovery process** rather than trying to know all tools upfront. GABS's design makes this discovery natural and reliable.