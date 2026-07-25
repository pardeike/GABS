# AI Integration Guide

This guide shows you how to connect GABS to different AI assistants and tools.

> **See also:** [Configuration Guide](CONFIGURATION.md) for setting up games, [OpenAI Tool Normalization](OPENAI_TOOL_NORMALIZATION.md) for OpenAI API compatibility, and [Deployment Guide](DEPLOYMENT.md) for production setups.
>
> If you are starting from a downloaded release archive, read [AI Client Setup Guide](AI_CLIENT_SETUP.md) first. It covers unzip/install steps, config locations, and ready-to-paste client snippets.
>
> **Building a launcher, container wrapper, or game-side bridge?** See [Launcher and Wrapper Contract](#launcher-and-wrapper-contract), [The Env-Only Live-Bridge Rule](#the-env-only-live-bridge-rule), and [Optional Delivery Report: the `observed` Field](#optional-delivery-report-the-observed-field) below.

## MCP Integration

GABS works as an MCP (Model Context Protocol) server. This means AI assistants can control your games through standard MCP tools.

### Available MCP Tools

Once GABS is running, AI can use these tools. Strict-safe names are advertised
by default; older dotted names remain accepted as call aliases.

- **`games_list`** - Show configured game IDs
- **`games_show`** - Show configuration and validation details for one game
- **`games_start`** - Start a game: `{"gameId": "factory"}`
- **`games_stop`** - Stop a game gracefully: `{"gameId": "factory"}`
- **`games_kill`** - Force quit a game: `{"gameId": "factory"}`
- **`games_status`** - Check if games are running: `{"gameId": "factory"}` or all games
- **`games_tool_names`** - Discover compact mirrored tool names
- **`games_tool_detail`** - Inspect one mirrored tool's schema
- **`games_tools`** - Fetch the richer compatibility listing of mirrored tools
- **`games_connect`** - Attach to a running game's GABP server after the bridge loads or after a GABS restart
- **`games_get_attention`** - Inspect a game's current blocking attention item
- **`games_ack_attention`** - Acknowledge the current blocking attention item and resume normal calls
- **`games_call_tool`** - Call a mirrored game tool through the stable core surface

Mirrored game tools are intentionally not advertised in the public `tools/list`
response. Discover them with `games_tool_names`, inspect one with
`games_tool_detail`, and call it through `games_call_tool`.

**Pro tip**: You can use either the game ID (`"adventure"`) or the launch target (`"123456"` for Steam) in any tool.

## Ownership and Reconnect Behavior

GABS coordinates live sessions per game with a short active-owner lease. If one
GABS session is actively using a running or starting game:

- `games_start` returns quickly instead of launching a duplicate copy
- `games_connect` returns quickly with an active-owner result instead of waiting
  on a competing bridge connection
- game-bound tool calls are blocked before they touch the bridge
- `games_status` may report that another GABS session owns the process and show
  the lease expiry
- `games_status` also reports runtime ownership and process-environment
  diagnostics without making `bridge.json` a recovery target
- if `games_start` reports `endpoint_cache_in_use`, use `games_connect` to
  attach to the already-listening endpoint or `resetEndpoint: true` only after
  confirming the cached endpoint should be rotated

Once the previous session is idle, `games_connect` naturally moves ownership to
the current session. If you intentionally want the current GABS session to take
ownership before the active lease expires, call:

```json
{
  "gameId": "adventure",
  "forceTakeover": true
}
```

with `games_connect`. This defaults to `false`.

## Attention-Aware Bridges

GABS is compatible with the additive attention surface introduced in GABP
v1.1 while staying on wire major `gabp/1`.

When a connected bridge advertises `attention/current` and `attention/ack`,
GABS can gate normal game-bound tool calls until the current attention item is
reviewed and acknowledged. The recovery flow is:

1. Call `games_get_attention`
2. Inspect the returned diagnostics or follow-up tooling
3. Call `games_ack_attention` with the returned `attentionId`
4. Retry the original game call

GABS still allows diagnostic and lifecycle observation tools through the gate so
an agent can understand the failure without disturbing the game further. Bridge
tools can opt into that behavior with generic `tools/list` tags such as
`diagnostic`, `health`, `lifecycle`, `observation`, `read-only`, `status`,
`telemetry`, or `attention-bypass`. Mutating gameplay and steering calls remain
blocked until attention is acknowledged.

## Launcher and Wrapper Contract

GABS makes one promise about launch context and keeps it precisely: it delivers
**argv, environment, and working directory** to the *first* process it spawns,
and only to that process. This is the one-hop guarantee — one syscall deep and
fully testable. Every hop after that (a shell script that execs the game, a
wrapper that starts a container, a store client that re-launches a child) is
owned by whoever creates it. If your launch goes through a wrapper, the wrapper
is responsible for carrying the context the rest of the way.

A conforming wrapper does three things.

**1. Forward argv to the real workload.** Pass the arguments through unchanged:

```sh
#!/bin/sh
# unix wrapper
exec /path/to/game "$@"
```

```bat
:: Windows wrapper (game.cmd)
@echo off
"C:\Games\game.exe" %*
```

`argv[0]` legitimately differs across the hop — GABS spawns the wrapper with the
wrapper as `argv[0]`, while the game sees its own executable there — so only the
argument payload *after* `argv[0]` needs to match.

**2. Preserve the environment, or map it explicitly across a filtering
boundary.** A plain script wrapper inherits the environment automatically and
needs to do nothing extra. A boundary that *filters* the environment — a
container, a sandbox — must re-inject the variables GABS set. GABS makes this
robust by exporting the exact list to carry:

- **`GABS_FORWARD_ENV`** — a comma-separated list of *every* variable name the
  wrapper must carry across a filtering boundary: the GABS-managed variables
  plus the names (never the values) of all config-defined context keys for this
  launch.

Forward them generically — never hardcode a fixed set — so the wrapper stays
correct when GABS or the config adds variables later:

```sh
#!/bin/sh
# container wrapper: re-inject every forwarded name inside the container.
# POSIX sh only (no bashisms): split the comma-separated list with IFS, then
# restore IFS so the space-separated $args expands correctly.
args=""
IFS=,
for v in $GABS_FORWARD_ENV; do
  args="$args -e $v"
done
unset IFS
exec docker run $args my-game-image "$@"
```

Passing `-e NAME` (name only, no value) tells the container runtime to read each
variable's value from the wrapper's own environment, so the values never appear
on the command line. The names are guaranteed to be portable identifiers — no
commas, whitespace, or glob characters — so splitting on the comma is always
safe. Use POSIX word-splitting (`IFS=,`) rather than a bash-only
`${GABS_FORWARD_ENV//,/ }` expansion, which fails under `/bin/sh` (dash) with
"Bad substitution" — exactly the shell a minimal container base image provides.

**3. Never reintroduce a name listed in `GABS_ABSENT_ENV`.**

- **`GABS_ABSENT_ENV`** — a comma-separated list of names that a profile
  deliberately *unset* and that must stay absent in the workload.

An absence cannot be forwarded, but a boundary can accidentally recreate one — a
container image whose build file defines `CONTENT_SET`, or a wrapper that
exports a default — and that defeats the isolation the profile intended. The
wrapper must not add these names back. Delivery verification (below) checks for
exactly this and reports a reintroduced name as a mismatch.

Both `GABS_FORWARD_ENV` and `GABS_ABSENT_ENV` are themselves in the forward set,
so a bridge running *inside* the container still receives both lists and can
build its optional delivery report.

For reference, the managed variables GABS injects into the first process include:

- **`GABS_GAME_ID`** — the configured game's identifier
- **`GABP_SERVER_PORT`** — port the game-side bridge must listen on
- **`GABP_TOKEN`** — per-launch authentication token for this GABP session
- **`GABS_BRIDGE_PATH`** — path hint for a bundled bridge, when configured
- **`GABS_PROFILE`** — the selected profile name, when a profile is used
- **`GABS_FORWARD_ENV`** — the forward list described above
- **`GABS_ABSENT_ENV`** — the must-be-absent list described above

Plus any config-defined context keys for the launch, and the platform variables
GABS pins where a platform needs them (for example `SystemRoot` on Windows). The
wrapper's job is the generic loop, not this list — treat these names as what a
game-side bridge *reads*, not a set to hardcode.

> There is deliberately no pre-launch "prepare" hook in GABS for materializing
> per-profile state (config files, directories, registry keys). A wrapper script
> *is* the prepare hook: it composes, runs under your control, and is testable
> without GABS.

## The Env-Only Live-Bridge Rule

The live GABP endpoint is discovered from **one source only: the injected
environment.** A game-side bridge takes its listening port from
`GABP_SERVER_PORT` and its per-launch token from `GABP_TOKEN`, both read from
the process environment. There is no other supported way to discover the live
endpoint.

**`bridge.json` is diagnostic only — never a discovery fallback.** GABS writes a
`bridge.json` file recording the endpoint, the selected profile, the config
revision, and the start time, but it exists for `doctor` output and human
debugging. A bridge must never read it to find its endpoint or token. The reason
is freshness: a file cannot prove which launch it belongs to. A process from a
previous launch — or a manually started one — that read the current
`bridge.json` would attach to the wrong generation and be mis-attributed to the
wrong launch and profile. Only the environment, delivered one hop deep at spawn,
proves which launch a process belongs to.

Two consequences follow, and GABS states them rather than hiding them:

- **A chain that strips the environment produces a workload whose bridge cannot
  attach at all.** There is no stale-file guess to fall back on. The fix is the
  wrapper contract above (forward the environment), never a file read.
- **Credentials are per-launch.** Every runtime claim mints a fresh `GABP_TOKEN`,
  even when the endpoint port is reused. A connection presenting a previous
  launch's token is rejected and surfaced as `stale_bridge_credential`, so a
  delayed process from a superseded launch can never authenticate into the new
  launch's session.

> Any one-time migration of a pre-upgrade endpoint out of a legacy `bridge.json`
> is a GABS-internal step inside `games_connect`, performed once under lock and
> validated by actually connecting. It is never something a bridge or wrapper
> does: for an implementer the rule has no exceptions — endpoint and token come
> from the environment.

See [GABP Bridge Development](GABP_BRIDGE_DEVELOPMENT.md) for the full game-side
bridge walkthrough.

## Optional Delivery Report: the `observed` Field

GABS is the GABP client; your game-side bridge is the GABP server. On the GABP
**session-welcome response**, a bridge MAY include one optional,
backward-compatible field — `observed` — reporting the raw launch context as
seen *inside* the game process. GABS uses it to verify that the resolved profile
actually reached the game.

The wire shape is:

```json
{
  "observed": {
    "argv": ["/path/as/seen/game", "--flag", "value"],
    "cwd": "/working/dir/as/seen",
    "envValues": { "GABP_SERVER_PORT": "12345", "WORLD_SEED": "42" },
    "envAbsent": ["CONTENT_SET"]
  }
}
```

How a bridge fills it:

- **`argv`** — the full argument vector as observed, *including* `argv[0]`.
  Report it raw; GABS excludes `argv[0]` itself before comparing, because
  element zero legitimately differs across launch hops.
- **`cwd`** — the working directory as observed, raw. GABS canonicalizes both
  sides (absolute, symlink-resolved, case- and separator-folded on Windows)
  before comparing.
- **`envValues`** — a map from each name in `GABS_FORWARD_ENV` to the value the
  bridge actually observed for it. If a name in `GABS_ABSENT_ENV` was
  reintroduced by a boundary and is present, report it here too — GABS flags
  that as the isolation failure it is.
- **`envAbsent`** — the list of `GABS_ABSENT_ENV` names the bridge checked and
  confirmed absent.

The three env states are explicit: a name in `envValues` was observed present
with that value; a name in `envAbsent` was checked and is absent; a name in
**neither** list was not reported.

**The bridge reports raw values and makes no judgment.** GABS hashes each
observed value locally against non-reversible, salted digests pinned at spawn,
compares per channel (argv, cwd, managed env, context env), decides pass/fail,
and discards the raw values. The bridge never hashes and never decides the
delivery outcome. (Bridge-side hashing was considered and rejected: it would
force distributing the salt and pinning a canonical encoding across every bridge
SDK for no real privacy gain on a token-authenticated localhost connection.)

**The whole field is entirely optional.** Omitting it is not a failure — it
simply yields an *unknown* (unverified) delivery for every channel, never a
`partial` and never an error. An older bridge that has never heard of `observed`
interoperates unchanged. When a bridge does report it, GABS surfaces a
per-channel `contextDelivery` verdict through `games_status`, so an operator can
confirm "did my profile actually reach the game" as an observed fact rather than
an inference.

> `envValues` can carry configured context values, which may be sensitive. The
> connection is token-authenticated and localhost-only, and GABS discards the
> values immediately after hashing — but secrets do not belong in launch inputs
> in the first place.

## Setting Up AI Assistants

### OpenAI API Integration

Strict-safe tool name normalization is enabled by default when
`toolNormalization` is omitted. Keep it enabled for OpenAI and Claude variants:

```json
{
  "toolNormalization": {
    "enableOpenAINormalization": true,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  }
}
```

This converts tool names like `factory.inventory.get` to
`factory_inventory_get` for client compatibility. See
[Tool Normalization Guide](OPENAI_TOOL_NORMALIZATION.md) for complete details.

Some clients also reject `outputSchema` fields in `tools/list`. If Claude Code
or another MCP client disconnects with an `outputSchema.type` validation error,
add this to `~/.gabs/config.json`:

```json
{
  "stripOutputSchema": true
}
```

### Claude Desktop

Add this to your Claude Desktop MCP settings:

```json
{
  "mcpServers": {
    "gabs": {
      "command": "/path/to/gabs",
      "args": ["server"]
    }
  }
}
```

Then you can ask Claude:
- "List all my configured games"
- "Start factory and check its status"
- "Stop all running games"

### Codex CLI

Add this to your configuration:

```toml
[mcp_servers.gabs]
command = "/path/to/gabs"
args = ["server"]
```

Each live Codex session runs its own stdio GABS process. Cross-session
coordination happens only when those sessions interact with the same configured
game.

### Custom AI Tools

Here's a Python example using an MCP client:

```python
import mcp_client

# Connect to GABS
client = mcp_client.connect_stdio(["/path/to/gabs", "server"])

# List all games
games = client.call_tool("games_list", {})
print("Available games:", games)

# Start a specific game
result = client.call_tool("games_start", {"gameId": "factory"})
print("Start result:", result)

# Check status
status = client.call_tool("games_status", {"gameId": "factory"})
print("Game status:", status)
```

## Deployment Scenarios

### Local Development Setup
Perfect when your AI and games run on the same computer:

```bash
# 1. Configure your games
gabs games add factory
gabs games add adventure

# 2. Start GABS MCP server
gabs server

# 3. Configure your AI to connect (see examples above)
# 4. Ask AI to control your games!
```

### Remote AI Access to Local GABS
For AI tooling that reaches GABS over HTTP while the games still run on your
local machine:

**On the machine running GABS and the games:**
```bash
# 1. Add games normally
gabs games add factory

# 2. Start GABS in HTTP mode
gabs server --http :8080
```

GABP itself remains local-only. Your game-side bridge still listens on
`127.0.0.1:GABP_SERVER_PORT`; only the MCP HTTP surface is exposed remotely.

**Configure your remote AI client:**
```json
{
  "mcpServers": {
    "remote-gabs": {
      "command": "curl",
      "args": ["-X", "POST", "http://your-computer-ip:8080/mcp", 
               "-H", "Content-Type: application/json",
               "-d", "@-"]
    }
  }
}
```

Use firewall rules, reverse proxy authentication, or a VPN before exposing the
HTTP endpoint outside your machine or LAN.

### Game Server Farm Management
Let AI manage multiple game servers:

```bash
# Configure multiple servers
gabs games add factory-survival
gabs games add factory-creative
gabs games add adventure-a
gabs games add adventure-b

# Start GABS
gabs server

# AI can now control all servers through MCP tools
```

## HTTP Mode for Web Integration

GABS can also run as an HTTP server for web-based AI tools:

```bash
# Start HTTP mode
gabs server --http localhost:8080
```

Then use standard HTTP requests:
```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0", 
    "id": 1, 
    "method": "tools/call", 
    "params": {
      "name": "games_list",
      "arguments": {}
    }
  }'
```

## Example AI Conversations

Here are some examples of what you can ask your AI once GABS is set up:

**Starting games:**
- "Start my FactorySim server"
- "Launch AdventureGame and check if it started correctly"
- "Start all my configured games"

**Managing games:**
- "Stop the FactorySim server gracefully"
- "Kill any frozen games"
- "Show me the status of all my games"

**Advanced usage:**
- "Start the survival server, wait 30 seconds, then check if players can connect"
- "Restart the creative server if it's using too much memory"
- "Start a backup world while keeping the main server running"

## Troubleshooting Integration

### "AI can't see GABS tools"
1. Make sure GABS server is running: `gabs server`
2. Check your AI's MCP configuration file
3. Restart your AI assistant after changing configuration

### "Connection refused"
1. Verify GABS is running on the expected port
2. Check firewall settings for HTTP mode
3. Make sure the path to GABS binary is correct

### "Game won't start from AI"
1. Test the game manually first: `gabs games start factory`
2. Check that your game configuration is working: `gabs games show factory`
3. Make sure your game-side bridge supports GABP

### "HTTP mode not working"
1. Check if the port is already in use
2. Try a different port: `gabs server --http :8081`
3. Verify your HTTP client is sending proper JSON-RPC requests
