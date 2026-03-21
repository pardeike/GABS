# AI Dynamic Tools FAQ - Answering Your GABS Questions

## The Question
> "At the start of the GABS server, it offers a basic set of mcp tools to an AI. This includes starting games for example. Once a game is started, GABS will try to connect to the games GABP server and will (hopefully) start mirroring the games mcp tools. Since I have yet to write such a mod (I am still developing GABS) it is unclear to me if at that point the amount of mcp tools will increase and if an AI will figure this out. How do you think this will work out in the real world when the amount of mcp tools is dynamic?"

> **See also:** [Dynamic Tools Guide](DYNAMIC_TOOLS_GUIDE.md) for technical implementation details, [AI Integration Guide](INTEGRATION.md) for connecting AI agents.

## The Answer: Yes, This Pattern Fits MCP Well

**TL;DR**: AI agents can handle GABS's dynamic tool expansion well because the
system uses clear namespacing, compact discovery tools, and a predictable
refresh pattern.

## Why AI Agents Will Handle This Well

### 1. **Built-in Discovery Mechanism**
GABS provides a compact two-step discovery flow specifically for this purpose:

```javascript
// AI discovers compact names for a specific game
const minecraftToolNames = await mcp.callTool("games.tool_names", {"gameId": "minecraft", "brief": true});

// AI inspects one candidate in detail
const inventoryDetail = await mcp.callTool("games.tool_detail", {
  "tool": "minecraft.inventory.get"
});

// The richer compatibility listing still exists when needed
const allGameTools = await mcp.callTool("games.tools", {});
```

This gives AI agents a predictable "discover names first, inspect details second" workflow, which is much more context-efficient than always dumping every schema up front. `games.tool_names` defaults to 50 names per page and can add one-line summaries in structured output with `brief: true`.

### 2. **Clear Namespacing Prevents Chaos**
Even with many tools from multiple games, AI agents can stay oriented because:

```
✅ Clear and Unambiguous:
- minecraft.inventory.get 
- minecraft.world.place_block
- rimworld.inventory.get
- rimworld.crafting.build
- valheim.inventory.get
- valheim.building.construct

❌ Confusing (what GABS avoids):
- inventory.get  (which game?)
- place_block   (Minecraft or Valheim?)
- build         (RimWorld crafting or Valheim building?)
```

### 3. **Progressive Disclosure Pattern**
AI agents don't see everything at once. Tools appear as they become relevant:

```
Phase 1: Stable core game-management tools
Phase 2: Mirrored tools appear after a game connects
Phase 3: More mirrored tools appear as more games connect
```

This is usually easier for AI agents than having every possible tool available
up front.

## Real-World AI Interaction Examples

### Example 1: Natural Discovery
```
User: "Help me with my Minecraft server"

AI: Let me see what I can do with Minecraft...
    → games.tool_names {"gameId": "minecraft", "brief": true}

AI: I can help you with:
    - Managing player inventories
    - World building and block placement  
    - Player teleportation
    - Chat and server control
    
    What would you like to do?

User: "Give me a diamond sword"

AI: I'll add that to your inventory...
    → minecraft.inventory.set {"playerId": "steve", "item": "diamond_sword", "count": 1}
```

### Example 2: Multi-Game Awareness  
```
User: "I'm running both Minecraft and RimWorld, help me manage both"

AI: Let me check what I can do with both games...
    → games.tool_names {"brief": true}

AI: Perfect! I can help you with:
    
    **Minecraft**:
    - Inventory management, world building, player control
    
    **RimWorld**:  
    - Colony management, crafting, research
    
    Both games are clearly separated, so I can help you with either.
    Which would you like to work with first?
```

### Example 3: Smart Tool Caching
```javascript
// AI agents can implement intelligent caching
class GABSClient {
  async refreshToolsWhenNeeded() {
    // Check if games have changed state
    const currentGames = await this.mcp.callTool("games.status", {});
    
    if (this.gameStateChanged(currentGames)) {
      // Refresh compact tool names
      this.availableTools = await this.mcp.callTool("games.tool_names", {"brief": true});
      this.lastRefresh = Date.now();
    }
  }
}
```

## Tested Proof: Dynamic Tool Discovery Works

The test suite covers the important discovery behaviors:

### Test Results Summary:
```
✅ AI starts with the stable core management surface
✅ AI discovers mirrored tools after a game connects
✅ AI uses games.tool_names and games.tool_detail to understand capabilities
✅ AI manages multiple connected games without tool-name collisions
✅ AI can recover after reconnects and session ownership changes
```

### AI Discovery Patterns That Work:
1. **Discovery-First**: Always check `games.tool_names` before attempting game actions
2. **Caching with Refresh**: Cache tools but refresh after game state changes
3. **Intent-Based Filtering**: Filter large tool sets by user intent
4. **Lazy Loading**: Only load tools for games the user is actually using

## Best Practices for AI Agents

### ✅ Recommended Patterns:
```javascript
// Pattern 1: Always discover before acting
async handleGameRequest(gameId, action) {
  const gameTools = await mcp.callTool("games.tool_names", {gameId, brief: true});
  const availableActions = parseToolsForCapabilities(gameTools);
  
  if (availableActions.includes(action)) {
    return await executeAction(gameId, action);
  } else {
    return `Sorry, ${action} isn't available for ${gameId}. Available: ${availableActions}`;
  }
}

// Pattern 2: Group tools by game for clarity
function organizeToolsForUser(allTools) {
  const gameGroups = {};
  for (const tool of allTools) {
    const [gameId, ...toolParts] = tool.name.split('.');
    if (!gameGroups[gameId]) gameGroups[gameId] = [];
    gameGroups[gameId].push(tool);
  }
  return gameGroups;
}
```

### ❌ Anti-Patterns to Avoid:
```javascript
// Don't cache tools indefinitely
const toolsCache = await mcp.callTool("games.tool_names", {}); // DON'T cache forever

// Don't assume tools exist without checking
await mcp.callTool("minecraft.inventory.get", {...}); // Check games.tool_names first!

// Don't overwhelm users with massive tool lists
console.log("Here are all available tools..."); // Group by game instead!
```

## The Technical Foundation

### GABS Architecture Supports This:
1. **MCP Compliance**: Standard JSON-RPC with proper tool metadata
2. **Game Prefixing**: Automatic namespacing prevents conflicts
3. **Discovery APIs**: `games.tool_names` and `games.tool_detail` provide structured exploration
4. **Status Awareness**: AI can check game state before tool usage
5. **Mirror System**: Automatic GABP→MCP tool conversion
6. **Session Ownership Guardrails**: Duplicate `games.start` and `games.connect`
   requests return quickly instead of racing a second live GABS session

## What About Multiple GABS Sessions?

This matters in the real world because developers often have more than one AI
session open.

- If one live GABS session already owns a running or starting game,
  `games.start` returns quickly instead of launching a duplicate process
- `games.connect` also returns quickly instead of hanging on a competing bridge
  connection
- `games.status` can report that another GABS session owns the process
- If takeover is intentional, `games.connect {"gameId": "rimworld",
  "forceTakeover": true}` moves ownership to the current session

### Current State
- **Tool change notifications are live**: AI clients get MCP
  `tools/list_changed` when mirrored tools appear or refresh.
- **Basic game resource mirroring is live**: GABS exposes per-game MCP
  resources such as event logs and current game state, and sends
  `resources/list_changed` when that surface changes.
- **Attention-aware guardrails are live**: when a bridge publishes blocking
  attention, GABS can pause normal game-bound calls until the client inspects
  and acknowledges the item.

### Still Evolving
- **Richer event streaming**: real-time event transport keeps improving as
  bridges expose more event capabilities.
- **Broader mirrored resources**: mods can keep expanding the game-specific
  resource surface beyond the baseline state and log resources.
- **Batch operations**: multi-action atomic workflows are still future work.

## Conclusion: Your Concern is Valid but Solved

The concern is valid: dynamic tool expansion can be confusing if the system does
not provide structure. GABS provides that structure.

### Why It Works:
1. **Predictable Discovery**: `games.tool_names` makes tool exploration systematic
2. **Clear Namespacing**: Game prefixes eliminate all ambiguity
3. **Progressive Disclosure**: Tools appear as capabilities are needed, not all at once
4. **Standard MCP**: AI agents already know how to handle MCP tool expansion
5. **Logical Grouping**: Tools cluster by game, making mental models easy

### Real-World Outcome:
AI agents working with GABS will naturally develop patterns like:

1. **Start with basics**: Use core `games.*` tools to manage games
2. **Discover capabilities**: Use `games.tool_names`, then `games.tool_detail` for the few tools you want to inspect; fully qualified tool names let you omit `gameId` in the detail and call steps
3. **Use clear names**: Always use game-prefixed tool names for clarity
4. **Cache intelligently**: Refresh tool knowledge after game state changes

This is often a better experience than a huge static tool set because the AI
can grow capabilities as games actually connect.

### Evidence:
The test suite exercises staged discovery, multi-game namespacing, reconnects,
and ownership changes without depending on a fixed tool count.

**Bottom line**: the dynamic tool system is workable in practice because GABS
gives AI clients a stable core surface, explicit discovery tools, and clear
namespacing.
