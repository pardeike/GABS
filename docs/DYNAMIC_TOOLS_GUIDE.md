# Dynamic Tool Discovery Guide for AI Agents

## Understanding GABS Dynamic Tool System

GABS uses a **progressive tool disclosure** system that helps AI agents handle the dynamic expansion of game capabilities as games connect. The public `tools/list` response stays stable and core-only; mirrored game tools are discovered through GABS tools instead of client-side tool-list churn.

> **Related guides:** [AI Integration Guide](INTEGRATION.md) for connecting AI agents, [Configuration Guide](CONFIGURATION.md) for game setup, [AI Dynamic Tools FAQ](AI_DYNAMIC_TOOLS_FAQ.md) for common questions.

## The Progressive Tool Discovery Pattern

### Phase 1: Initial State (Core Game Management)
When GABS starts, AI agents see only core game management tools:

```
Stable core tools:
- games_list          - List configured game IDs
- games_show          - Inspect one configured game
- games_start         - Start a game
- games_stop          - Stop a game gracefully
- games_kill          - Force terminate a game
- games_status        - Check game status
- games_tool_names    - Compact mirrored-tool discovery
- games_tool_detail   - Detailed schema for one tool
- games_tools         - Rich compatibility listing
- games_connect       - Reattach to a running game's GABP server
- games_get_attention - Inspect the current blocking attention item
- games_ack_attention - Acknowledge attention and resume normal calls
- games_call_tool     - Call a mirrored tool through the stable core surface
```

Legacy dotted names such as `games.list` are accepted as call aliases, but
strict-safe names are what `tools/list` advertises by default.

### Phase 2: Game Connection (Dynamic Discovery)
After starting a game with GABP bridges, the discovery surface expands:

```
games_tool_names and games_tool_detail expose mirrored game tools. The exact set
depends on the connected game and bridge:

FactorySim-Specific Examples:
- factory_inventory_get     - Get player inventory
- factory_inventory_set     - Modify player inventory
- factory_world_get_block   - Check block at position
- factory_world_place_block - Place block in world
- factory_player_teleport   - Teleport player
- factory_chat_send         - Send chat message
- factory_time_set          - Set world time
- factory_weather_set       - Control weather

AdventureGame-Specific Tools (if adventure also starts):
- adventure_inventory_get      - Get party inventory
- adventure_crafting_build     - Build items/structures
- adventure_party_command   - Give party commands
- adventure_quest_progress  - Check quest status
```

GABS can also have multiple live sessions. Runtime ownership is a short
active-owner lease, not a permanent lock on an AI client session. If another session is
actively using a running game, `games.start`, `games.connect`, and game-bound
tool calls return quickly instead of competing. Once that lease goes idle,
`games.connect` naturally moves ownership to the current session. Use
`games.connect {"forceTakeover": true}` only when intentionally overriding an
active owner immediately.

When a game is not connecting cleanly, call `games_status` first. Its structured
diagnostics report GABS runtime ownership and the game process environment
where available. Follow `nextActions` rather than inspecting or editing
`bridge.json`; that file is GABS' endpoint cache/debug artifact. If
`games_start` reports `endpoint_cache_in_use`, use `games_connect` to attach or
`resetEndpoint: true` only after confirming the cache should be rotated.

For low-latency startup loops, `games_start` returns after the GABP handshake
instead of waiting for full tool mirroring. The mirror refresh runs in the
background, the public `tools/list` response remains stable, and
`games_call_tool` can still dispatch a fully qualified slash or dotted connected
game tool immediately. Use `games_tool_names` when you need discovery; skip it
when the next command name is already known.

For games with slow bridge startup, pass a larger `timeout` to `games_start` or
configure `timeouts.startup.gabpConnectSeconds` to increase the total connection
budget. `games_start` still returns after a bounded initial wait; GABS keeps
trying in the background and `games_connect` can adopt a running process
environment if a launcher reused older GABP values.

## AI Discovery Strategies

### 1. The `games_tool_names` -> `games_tool_detail` Discovery Pattern (Recommended)

**Best Practice**: Use `games_tool_names` for low-token discovery, then call `games_tool_detail` only for the few tools you might actually use. Keep `games_tools` for compatibility or when you intentionally want the richer one-shot listing.

```javascript
// AI Discovery Workflow
async function discoverGameCapabilities() {
  // 1. See what games are available
  const games = await mcp.callTool("games_list", {});

  // 2. Check what games are running
  const status = await mcp.callTool("games_status", {});

  // 3. Discover compact tool names for each running game
  const allToolNames = await mcp.callTool("games_tool_names", {"brief": true});

  // 4. Get specific names for the game of interest
  const factoryToolNames = await mcp.callTool("games_tool_names", {"gameId": "factory", "brief": true});

  // 5. Inspect one candidate in detail only when needed
  const inventoryDetail = await mcp.callTool("games_tool_detail", {
    "tool": "factory_inventory_get"
  });

  return { games, status, allToolNames, factoryToolNames, inventoryDetail };
}
```

### 2. Progressive Enhancement Pattern

AI agents should treat tool discovery as a progressive enhancement:

```
Phase 1: Use core tools to start games
"Start the FactorySim server" -> games_start {"gameId": "factory"}

Phase 2: Discover what's now possible
"What can I do with FactorySim now?" -> games_tool_names {"gameId": "factory", "brief": true}

Phase 2b: Inspect one candidate when needed
"What does factory_inventory_get accept?" -> games_tool_detail {"tool": "factory_inventory_get"}

Phase 3: Use discovered tools
"Get Steve's inventory" -> factory_inventory_get {"playerId": "steve"}
```

### 3. Context-Aware Tool Usage

Tools are namespaced by game ID to prevent conflicts:

```
✅ Clear Intent:
- factory_inventory_get {"playerId": "steve"}      # Steve's FactorySim inventory
- adventure_inventory_get {"partyMemberId": "alice"}     # Alice's AdventureGame inventory

❌ Ambiguous (avoided by design):
- inventory_get {"playerId": "steve"}                # Which game?
```

## Real-World AI Interaction Examples

### Example 1: Starting Fresh
```
User: "Help me manage my games"
AI: Let me see what games you have configured...
    -> games_list

AI: I see you have factory and adventure configured. Neither is running.
    Would you like me to start one of them?

User: "Start FactorySim"
AI: Starting FactorySim...
    -> games_start {"gameId": "factory"}

AI: FactorySim started! Let me see what I can help you with...
    -> games_tool_names {"gameId": "factory", "brief": true}

AI: Great! Now I can help you with:
    - Managing your inventory (factory_inventory_get/set)
    - Building in your world (factory_world_place_block)
    - Teleporting around (factory_player_teleport)
    - Controlling time and weather
    What would you like to do?
```

### Example 2: Multi-Game Management
```
User: "I want to run both games and manage them"
AI: I'll start both games and show you the expanded capabilities...
    -> games_start {"gameId": "factory"}
    -> games_start {"gameId": "adventure"}
    -> games_tool_names {"brief": true}

AI: Perfect! Now I can help you with:

    FactorySim:
    - Inventory management
    - World building
    - Player control

    AdventureGame:
    - Party management
    - Crafting systems
    - Quest progress

    The tools are clearly separated by game name, so I can help
    you manage both simultaneously without confusion.
```

### Example 3: Discovery-Driven Assistance
```
User: "What's possible with my FactorySim server?"
AI: Let me check what your FactorySim bridge exposes...
    -> games_tool_names {"gameId": "factory", "brief": true}
    -> games_tool_detail {"tool": "factory_inventory_get"}

AI: Your FactorySim setup provides these capabilities:
    - Inventory System: Get/modify player inventories
    - World Interaction: Place/check blocks, teleport players
    - Server Control: Manage time, weather, send chat messages

    This suggests you have a comprehensive admin/creative bridge installed.
    What would you like me to help you with?

User: "Give me a diamond sword"
AI: I'll add a diamond sword to your inventory...
    -> factory_inventory_set {"playerId": "steve", "item": "diamond_sword", "count": 1}
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
    const tools = await this.mcp.callTool("games_tool_names", {"brief": true});
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

    // 3. If the next tool name is known, call it via games_call_tool immediately.
    // Refresh tool knowledge only when discovery is needed.
    await refreshAvailableTools();
  }

  // 5. Execute with discovered tools
  return await executeWithDiscoveredTools(intent);
}
```

## Best Practices for AI Agents

### ✅ Do:
1. **Use `games_tool_names` for discovery** before attempting game-specific actions; add `brief: true` when a one-line structured summary helps ranking
2. **Cache discovery results** but refresh periodically or after game state changes
3. **Use `games_tool_detail`** only for the few tools you might actually call; omit `gameId` when the tool name is already fully qualified
4. **Handle discovery changes gracefully** - treat new game tools as opportunities
5. **Use game-prefixed tool names** explicitly to avoid ambiguity
6. **Group tools by game** in user interfaces for clarity

### ❌ Don't:
1. **Don't assume tools exist** without checking via `games_tool_names`
2. **Don't cache tools indefinitely** - they change as games start/stop
3. **Don't try to use generic tool names** like `inventory.get`
4. **Don't overwhelm users** with massive detailed tool dumps - group and filter intelligently
5. **Don't treat duplicate starts as success signals** - if another live GABS
   session is actively using the game, expect `games_start`, `games_connect`,
   or game-bound calls to return a quick active-owner message. If the previous
   session is idle, `games_connect` can take ownership naturally.

## GABS Design Advantages for AI

The GABS architecture specifically helps AI agents handle dynamic tools:

1. **Clear Namespacing**: `factory_inventory_get` vs `adventure_inventory_get` eliminates ambiguity
2. **Discovery Tools**: `games_tool_names` and `games_tool_detail` provide structured tool exploration
3. **Stable Core Surface**: `tools/list` stays small while game tools appear in discovery results
4. **Status Awareness**: AI can check game state before attempting tool use
5. **Fallback Gracefully**: Core game management always available

## Conclusion

AI agents working with GABS will handle dynamic game capability expansion effectively because:

1. **The discovery pattern is predictable** - start with `games_tool_names`
2. **Tool names are unambiguous** - game prefixes prevent conflicts
3. **Expansion is progressive** - discovery results update as capabilities are needed
4. **The core remains stable** - basic game management always works

The key insight is that AI agents should **embrace the discovery process** rather than trying to know all tools upfront. GABS's design makes this discovery natural and reliable.
