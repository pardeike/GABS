# AI Dynamic Tools FAQ - Answering Your GABS Questions

## The Question
> "At the start of the GABS server, it offers a basic set of mcp tools to an AI. This includes starting games for example. Once a game is started, GABS will try to connect to the games GABP server and will (hopefully) start mirroring the games mcp tools. Since I have yet to write such a game-side bridge (I am still developing GABS) it is unclear to me if at that point the amount of mcp tools will increase and if an AI will figure this out. How do you think this will work out in the real world when the amount of mcp tools is dynamic?"

> **See also:** [Dynamic Tools Guide](DYNAMIC_TOOLS_GUIDE.md) for technical implementation details, [AI Integration Guide](INTEGRATION.md) for connecting AI agents.

## The Answer: Yes, This Pattern Fits MCP Well

**TL;DR**: AI agents can handle GABS's dynamic game capabilities well because
the public MCP tool list stays stable while mirrored game tools are exposed
through clear namespacing, compact discovery tools, and predictable refreshes.

## Why AI Agents Will Handle This Well

### 1. **Built-in Discovery Mechanism**
GABS provides a compact two-step discovery flow specifically for this purpose:

```javascript
// AI discovers compact names for a specific game
const factoryToolNames = await mcp.callTool("games_tool_names", {"gameId": "factory", "brief": true});

// AI inspects one candidate in detail
const inventoryDetail = await mcp.callTool("games_tool_detail", {
  "tool": "factory_inventory_get"
});

// The richer compatibility listing still exists when needed
const allGameTools = await mcp.callTool("games_tools", {});
```

This gives AI agents a predictable "discover names first, inspect details second" workflow, which is much more context-efficient than always dumping every schema up front. `games_tool_names` defaults to 50 names per page and can add one-line summaries in structured output with `brief: true`.

### 2. **Clear Namespacing Prevents Chaos**
Even with many tools from multiple games, AI agents can stay oriented because:

```
✅ Clear and Unambiguous:
- factory_inventory_get
- factory_world_place_block
- adventure_inventory_get
- adventure_crafting_build
- arena_inventory_get
- arena_building_construct

❌ Confusing (what GABS avoids):
- inventory.get  (which game?)
- place_block   (FactorySim or AdventureArena?)
- build         (AdventureGame crafting or AdventureArena building?)
```

### 3. **Progressive Disclosure Pattern**
AI agents don't need to see everything at once. Discovery results appear as game
capabilities become relevant:

```
Phase 1: Stable core game-management tools
Phase 2: Mirrored tools appear in games_tool_names after a game connects
Phase 3: More mirrored tools appear in discovery as more games connect
```

This is usually easier for AI agents than having every possible tool available
up front.

## Real-World AI Interaction Examples

### Example 1: Natural Discovery
```
User: "Help me with my FactorySim server"

AI: Let me see what I can do with FactorySim...
    -> games_tool_names {"gameId": "factory", "brief": true}

AI: I can help you with:
    - Managing player inventories
    - World building and block placement
    - Player teleportation
    - Chat and server control

    What would you like to do?

User: "Give me a diamond sword"

AI: I'll add that to your inventory...
    -> factory_inventory_set {"playerId": "steve", "item": "diamond_sword", "count": 1}
```

### Example 2: Multi-Game Awareness
```
User: "I'm running both FactorySim and AdventureGame, help me manage both"

AI: Let me check what I can do with both games...
    -> games_tool_names {"brief": true}

AI: Perfect! I can help you with:

    **FactorySim**:
    - Inventory management, world building, player control

    **AdventureGame**:
    - Party management, crafting, research

    Both games are clearly separated, so I can help you with either.
    Which would you like to work with first?
```

### Example 3: Smart Tool Caching
```javascript
// AI agents can implement intelligent caching
class GABSClient {
  async refreshToolsWhenNeeded() {
    // Check if games have changed state
    const currentGames = await this.mcp.callTool("games_status", {});

    if (this.gameStateChanged(currentGames)) {
      // Refresh compact tool names
      this.availableTools = await this.mcp.callTool("games_tool_names", {"brief": true});
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
✅ AI uses games_tool_names and games_tool_detail to understand capabilities
✅ AI manages multiple connected games without tool-name collisions
✅ AI can recover after reconnects and session ownership changes
```

### AI Discovery Patterns That Work:
1. **Discovery-First**: Always check `games_tool_names` before attempting game actions
2. **Caching with Refresh**: Cache tools but refresh after game state changes
3. **Intent-Based Filtering**: Filter large tool sets by user intent
4. **Lazy Loading**: Only load tools for games the user is actually using

## Best Practices for AI Agents

### ✅ Recommended Patterns:
```javascript
// Pattern 1: Always discover before acting
async handleGameRequest(gameId, action) {
  const gameTools = await mcp.callTool("games_tool_names", {gameId, brief: true});
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
const toolsCache = await mcp.callTool("games_tool_names", {}); // DON'T cache forever

// Don't assume tools exist without checking
await mcp.callTool("factory_inventory_get", {...}); // Check games_tool_names first!

// Don't overwhelm users with massive tool lists
console.log("Here are all available tools..."); // Group by game instead!
```

## The Technical Foundation

### GABS Architecture Supports This:
1. **MCP Compliance**: Standard JSON-RPC with proper tool metadata
2. **Game Prefixing**: Automatic namespacing prevents conflicts
3. **Discovery APIs**: `games_tool_names` and `games_tool_detail` provide structured exploration
4. **Status Awareness**: AI can check game state before tool usage
5. **Mirror System**: Automatic GABP→MCP tool conversion
6. **Session Ownership Guardrails**: Duplicate `games_start`, active
   `games_connect`, and game-bound calls return quickly instead of racing a
   second live GABS session

## What About Multiple GABS Sessions?

This matters in the real world because developers often have more than one AI
session open.

- If one live GABS session actively owns a running or starting game,
  `games_start` returns quickly instead of launching a duplicate process
- `games_connect` also returns quickly instead of hanging on a competing bridge
  connection while the active owner lease is still open
- once the previous session is idle, `games_connect` moves ownership to the
  current session without `forceTakeover`
- game-bound calls from old sessions are blocked while another active owner
  holds the lease
- `games_status` can report that another GABS session owns the process and show
  the lease expiry
- if `games_start` reports `endpoint_cache_in_use`, use `games_connect` to
  attach to the already-listening endpoint or `resetEndpoint: true` only after
  confirming the endpoint cache should be rotated
- if takeover is intentional before the active lease expires,
  `games_connect {"gameId": "adventure", "forceTakeover": true}` moves
  ownership to the current session

### Current State
- **Stable public tool list is live**: mirrored game tools no longer require
  MCP `tools/list_changed`; clients discover them with `games_tool_names` and
  call them with `games_call_tool`.
- **Basic game resource mirroring is live**: GABS exposes per-game MCP
  resources such as event logs and current game state, and sends
  `resources/list_changed` when that surface changes.
- **Attention-aware guardrails are live**: when a bridge publishes blocking
  attention, GABS can pause normal game-bound calls until the client inspects
  and acknowledges the item.

### Still Evolving
- **Richer event streaming**: real-time event transport keeps improving as
  bridges expose more event capabilities.
- **Broader mirrored resources**: game integrations can keep expanding the game-specific
  resource surface beyond the baseline state and log resources.
- **Batch operations**: multi-action atomic workflows are still future work.

## Conclusion: Your Concern is Valid but Solved

The concern is valid: dynamic tool expansion can be confusing if the system does
not provide structure. GABS provides that structure.

### Why It Works:
1. **Predictable Discovery**: `games_tool_names` makes tool exploration systematic
2. **Clear Namespacing**: Game prefixes eliminate all ambiguity
3. **Progressive Disclosure**: Tools appear as capabilities are needed, not all at once
4. **Standard MCP**: AI agents already know how to handle MCP tool expansion
5. **Logical Grouping**: Tools cluster by game, making mental models easy

### Real-World Outcome:
AI agents working with GABS will naturally develop patterns like:

1. **Start with basics**: Use core `games_*` tools to manage games
2. **Discover capabilities**: Use `games_tool_names`, then `games_tool_detail` for the few tools you want to inspect; fully qualified tool names let you omit `gameId` in the detail and call steps
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
