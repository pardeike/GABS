---
name: gabs-mcp
description: Use when working with the GABS MCP server to inspect configured games, start or reconnect game bridges, discover mirrored GABP tools through the stable core surface, call game-specific tools, or debug game-development loops through GABS.
---

# GABS MCP

Use GABS as the stable control surface for a local game-development loop. GABS starts configured games, connects to GABP-compatible bridges, mirrors bridge tools into MCP, and keeps game-specific discovery/calls on the stable core path through `games_tool_names` and `games_call_tool`.

## Default Workflow

1. Check configured games with `games_list`.
2. Inspect current state with `games_status`; pass `gameId` when you know it.
3. Start a stopped game with `games_start`, or attach to an already running game with `games_connect`.
4. Discover connected bridge tools with `games_tool_names` using `brief: true`.
5. Inspect one candidate with `games_tool_detail`.
6. Call the tool through `games_call_tool` unless a direct mirrored MCP tool is clearly available and already discovered.
7. After start, connect, stop, reconnect, or an error, call `games_status` or `games_tool_names` again instead of relying on cached tool lists.

## Tool Rules

- Prefer strict-safe names such as `games_status`, `games_tool_names`, and `games_call_tool`.
- Dotted names such as `games.status` may work as aliases, but do not use them as the first choice.
- Use `gameId` values from `games_list`; do not guess from display names.
- Use `games_show` when a game fails to start or stop, especially for Steam/Epic `stopProcessName` validation.
- Use `games_connect` after a game is already open, after a GABS restart, or when `games_start` says the process is running but GABP was not ready.
- Use `games_connect` with `forceTakeover: true` only when intentionally moving ownership from another live GABS session.
- For games with slow bridge startup, pass a larger `timeout` to `games_start` or configure `timeouts.startup.gabpConnectSeconds` so startup waits for the GABP bridge instead of returning early.

## Discovery

- Use `games_tool_names` before attempting game-specific actions.
- Pass `brief: true` for compact summaries.
- Pass `query` or `prefix` when looking for a likely capability.
- Use `games_tool_detail` for the exact schema of one tool before supplying arguments.
- If `games_tool_detail` or `games_call_tool` says a tool is missing, inspect the structured candidates or call `games_tool_names` again.

## Calling Game Tools

- Prefer `games_call_tool` with:
  - `gameId` when the tool name is local or ambiguous;
  - `tool` from `games_tool_names` or `games_tool_detail`;
  - `arguments` matching `games_tool_detail.inputSchema`;
  - `timeout` for long-running game actions.
- Fully qualified slash or dotted GABP names can be sent through `games_call_tool` before direct mirrored MCP tools appear.
- If a call is blocked by attention, use `games_get_attention`, decide what to do, then acknowledge with `games_ack_attention` when appropriate.
- While attention is open, diagnostic and lifecycle observation tools may still be callable through `games_call_tool`; use them to inspect bridge status, operations, logs, game-loaded state, or long-event idle state before acknowledging.
- Bridge authors should mark such tools with generic tags like `diagnostic`, `read-only`, `status`, `health`, `lifecycle`, or `attention-bypass`; do not rely on bridge-specific tool names.

## Recovery

- If no bridge tools are listed, call `games_status` first, then `games_connect` if the game is running.
- If GABP disconnected, call `games_status` to inspect the last disconnect note, then `games_connect` after the bridge is ready.
- If a client rejected `tools/list`, check whether `stripOutputSchema` is enabled in GABS config.
- If stop or kill fails for launcher games, use `games_show` and fix `stopProcessName`.
