# GABS Agent Guide

GABS is a generic Game Agent Bridge Server. It starts configured local games,
tracks their lifecycle, connects to GABP-compatible game-side bridges, and
exposes a stable MCP surface for AI clients.

Keep GABS generic. Do not bake a specific game, bridge package, local setup,
store app ID, process name, or tool namespace into runtime behavior, docs,
examples, tests, or skills unless the test is explicitly about accepting
arbitrary user-provided names. Prefer neutral examples such as `factory`,
`adventure`, `ExampleGameBridge`, `GameName.exe`, and `123456`.

Avoid legacy game-package-extension wording in public GABS surfaces. Use `game-side bridge`,
`GABP bridge`, `game integration`, or `bridge integration` depending on context.

## Architecture

GABS has two server/client roles:

- MCP layer: GABS is the MCP server for AI clients.
- GABP layer: the game-side bridge is the GABP server, and GABS is the GABP
  client.

The game-side bridge should read runtime connection data from environment
variables first:

- `GABP_SERVER_PORT`
- `GABP_TOKEN`
- `GABS_GAME_ID`
- `GABS_BRIDGE_PATH` as fallback/debug metadata only

`runtime.json` belongs to GABS ownership coordination. Game-side bridge code
must ignore it.

## Working Rules

- Build with `make build` after Go source changes.
- Run `go test ./...` after behavior changes.
- Keep public MCP tool names strict-safe by default; dotted names are accepted
  as compatibility aliases.
- Prefer `games_tool_names`, `games_tool_detail`, and `games_call_tool` for
  mirrored game tools instead of relying on dynamic public `tools/list` churn.
- If startup or reconnect behavior changes, verify `games_status` structured
  diagnostics and `nextActions`.
- If public docs or skills change, scan `README.md`, `docs/*`,
  `skills/gabs-mcp/SKILL.md`, `example-config.json`, and `AGENTS.md` for
  setup-specific names before finishing.

## Attention Gate

GABP v1.1 bridges may expose `attention/current` and `attention/ack`. When a
blocking attention item is open, GABS blocks normal game-bound calls until the
agent reviews and acknowledges it.

Do not hardcode bridge-specific diagnostic tool names in GABS. A bridge can
mark observation tools with generic tags in `tools/list`, and GABS will use
those tags to decide whether a tool may bypass the attention gate.

Recognized attention-bypass tags include:

- `attention-bypass`
- `diagnostic`
- `diagnostics`
- `health`
- `lifecycle`
- `observation`
- `read-only`
- `status`
- `telemetry`

Name-based fallback is intentionally generic and only looks for broad
diagnostic/lifecycle tokens such as `status`, `logs`, `operation`, `health`,
`ping`, `ready`, and `idle`.

## Future-Proofing

When a local bridge needs special treatment, first ask what generic protocol
signal GABS should consume. Add metadata, tags, config, or a protocol capability
instead of matching that bridge's names. Then document the generic contract and
add a regression test that uses neutral example names.
