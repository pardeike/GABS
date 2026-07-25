# Configuration Guide

This guide shows you how to add games to GABS and what the important config
fields mean. It covers the classic single-launch setup **and** the launch-
profile feature set: named launch profiles, typed launch inputs, and lifecycle
hooks.

If you only run one launch of one game, read [Quick Setup](#quick-setup) and
[Launch Modes](#launch-modes-explained) and stop there. Everything from
[Launch Profiles](#launch-profiles) onward is optional and additive: existing
configs stay valid, untouched, and warning-free.

## Quick Setup

For most users, this is the only command you need:

```bash
gabs games add factory
```

GABS will ask a few questions and save the result to your local config file.

## What GABS Asks You

When you run `gabs games add <game-id>`, GABS asks for:

### 1. Game Name
A friendly label such as `Example Game` or `AdventureGame`.

### 2. Launch Mode
How GABS should start the game:

- **DirectPath**: a local executable or script
- **SteamManaged**: a Steam App ID resolved to the installed game executable
- **SteamAppId**: a legacy Steam launcher URL for compatibility
- **EpicAppId**: an Epic Games Store ID
- **CustomCommand**: a custom command with arguments

### 3. Target
The executable path, App ID, or command for the selected launch mode:

- For DirectPath: `/path/to/game.exe`
- For Steam: `123456` (the App ID)
- For Epic: the Epic App ID
- For Custom: your complete command

### 4. Working Directory (Optional)
Where the game should run from. Leave blank to use the game's default location.

### 5. Stop Process Name
The real game process name used by `games_stop` and `games_kill`.

This is required for `SteamAppId` and `EpicAppId` launcher modes. Without it,
GABS can launch the game but cannot stop the actual game process reliably. (A
game-level `status` hook plus a `stop` or `kill` hook can satisfy this
requirement instead — see [Lifecycle Hooks](#lifecycle-hooks).)

Examples:
- For AdventureGame: `GameName.exe` (Windows) or `AdventureGame` (Linux/macOS)
- For FactorySim with Java: `java`
- For engine-based games: often the game name with a `.exe` extension

### 6. Save and verify

After setup, verify the saved config:

```bash
gabs games list
gabs games show factory
```

## What Most Users Need To Know

- GABS stores your games in `~/.gabs/config.json`
- GABS starts games using the launch data you entered above
- If the game also has a GABP-compatible bridge, GABS can connect to that
  bridge and mirror game-specific tools into MCP
- Config edits apply automatically. There is no reload command: after you edit
  the file, run `gabs games show <id>` (or `games_show`) and the new state,
  revision, warnings, or exact error is right there.

## Configuration File

Your games are saved in `~/.gabs/config.json`.

The top-level `"version"` field is the GABS config schema version, not the GABP
wire version. It stays `"1.0"`; the launch-profile fields are optional
additions, so no version bump is needed.

Minimal example:

```json
{
  "version": "1.0",
  "toolNormalization": {
    "enableOpenAINormalization": false,
    "maxToolNameLength": 64,
    "preserveOriginalName": true
  },
  "stripOutputSchema": false,
  "timeouts": {
    "startup": {
      "processStartSeconds": 10,
      "gabpConnectSeconds": 60
    }
  },
  "games": {
    "factory": {
      "id": "factory",
      "name": "Example Game",
      "launchMode": "DirectPath",
      "target": "/opt/factory/start.sh",
      "workingDir": "/opt/factory",
      "stopProcessName": "java",
      "description": "Main FactorySim server"
    },
    "adventure": {
      "id": "adventure",
      "name": "AdventureGame",
      "launchMode": "SteamManaged",
      "target": "123456"
    }
  }
}
```

### Top-level settings: hot vs restart-required

- Everything under `games` is **hot**: edits apply on the next
  config-dependent call without restarting GABS.
- Top-level settings (`apiKey`, `toolNormalization`, `portRanges`, `timeouts`,
  `stripOutputSchema`) are read at **startup** and require a restart to change.
- An invalid config file keeps the last valid snapshot in memory: new starts
  are refused with the exact error and JSON path, read-only calls still
  succeed and surface `configError`, and stop/kill/status of already-active
  launches keep working from their pinned runtime snapshots. Fixing the file
  clears the condition on the next call.
- Each launch pins exactly one config snapshot. Editing or deleting a profile
  never changes how an already-running launch is observed or stopped.

## Launch Modes Explained

### DirectPath
Best for custom game installs, scripts, and local test setups. GABS delivers
argv, environment, and working directory to the process it spawns.

```json
{
  "launchMode": "DirectPath",
  "target": "/home/user/games/factory/start.sh",
  "workingDir": "/home/user/games/factory"
}
```

### SteamManaged
Best for Steam games with GABP bridges.

```json
{
  "launchMode": "SteamManaged",
  "target": "123456"
}
```

You can find the App ID in the game's store URL. GABS locates the library,
reads the app manifest, starts the client if needed, launches the resolved
executable with GABP environment variables, and prepares `steam_appid.txt`
when direct Steamworks startup requires it. Configured `args` and `env` are
passed to the game in this mode.

**Re-exec caveat.** If Steam or the platform relaunches the final game process
through its own client, the injected environment and argv can be dropped — see
[Steam re-exec caveat and workarounds](#steam-re-exec-caveat-and-workarounds).
When this happens, `games_status` reports it (it is no longer silent), and the
[delivery verification](#does-my-context-actually-reach-the-game) surfaces
exactly which channel was lost.

Use `gabs games doctor <id>` to inspect the resolved executable and
`gabs games repair <id>` to switch an older `SteamAppId` config to this mode.

### SteamAppId
Legacy Steam launcher URL mode.

```json
{
  "launchMode": "SteamAppId",
  "target": "123456",
  "stopProcessName": "GameName.exe"
}
```

GABS starts the game through the platform launcher URL. This mode hands a URL
to the OS opener and **promises no context propagation**: configured `args`,
`env`, `unsetEnv`, `profiles`, and `launchInputs` cannot reach the game and are
rejected as config errors (see [URL launch modes](#url-launch-modes-and-their-limits)).
Put launch options such as `-savedatafolder=...` in Steam's own launch
options, or use `SteamManaged`, `DirectPath`, or `CustomCommand` when GABS must
control process arguments and bridge environment directly. `stopProcessName`
(or a status + stop/kill hook) is required.

### EpicAppId
Best for games installed through the Epic Games Store.

```json
{
  "launchMode": "EpicAppId",
  "target": "your-epic-app-id",
  "stopProcessName": "GameName.exe"
}
```

Like `SteamAppId`, this is a URL launcher mode: it cannot propagate `args`,
`env`, or a working directory to the game, and it rejects `profiles`/
`launchInputs`/`env`/`unsetEnv`. Use the launcher's own launch options,
`DirectPath`, or `CustomCommand` for process arguments. `stopProcessName` (or a
status + stop/kill hook) is required.

### CustomCommand
Best for complex launch setups or special requirements. GABS delivers argv,
environment, and working directory to the process it spawns.

```json
{
  "launchMode": "CustomCommand",
  "target": "java -Xmx4G -jar server.jar --nogui",
  "workingDir": "/opt/factory"
}
```

---

# Launch Profiles

A single game entry can describe **many launches** of the same game without
duplicating the entry. This is the launch-profile feature set. It adds six
optional game-level fields, all of which default to "off":

| Field | What it does |
|-------|--------------|
| `env` | Base environment applied to every launch of this game |
| `unsetEnv` | Base environment keys to remove before launch |
| `profiles` | Named launch-context overlays (data root, content set, hooks) |
| `defaultProfile` | Which profile a bare `games_start <id>` uses |
| `launchInputs` | Named, typed, caller-suppliable startup options |
| `lifecycle` | `status` / `stop` / `kill` hooks that observe and control the workload |

The organizing principle that decides where a setting belongs:

> **Anything a stop/kill/status action must know to find the workload** — a
> container name, a service, a data root — **is a profile.** Anything that only
> changes what the workload does at startup is a **launch input.** Launch
> inputs must be lifecycle-neutral: GABS cannot infer executable semantics, so
> a value the lifecycle would need to identify the workload must be a profile
> instead.

Full example (this is the shape the rest of this section explains):

```json
{
  "id": "adventure",
  "name": "Adventure Game",
  "launchMode": "DirectPath",
  "target": "/opt/example/adventure",
  "args": ["--bridge"],
  "env": { "LOG_FORMAT": "json" },
  "defaultProfile": "vanilla",
  "profiles": {
    "vanilla": {
      "description": "Untouched user data",
      "args": ["--data-root", "/srv/adventure/vanilla"]
    },
    "combat-test": {
      "description": "Isolated combat-test data",
      "args": ["--data-root", "/srv/adventure/combat-test"],
      "env": { "CONTENT_SET": "combat" },
      "workingDir": "/srv/adventure/combat-test"
    }
  },
  "launchInputs": {
    "quickStart": {
      "description": "Skip menus and load the configured quick-start path",
      "type": "boolean",
      "args": ["--quick-start"]
    },
    "scenario": {
      "description": "Select a configured startup scenario",
      "type": "string",
      "enum": ["arena", "tutorial"],
      "profiles": ["combat-test"],
      "args": ["--scenario", "${value}"]
    }
  },
  "lifecycle": {
    "status": { "command": "adventure-status", "args": ["${profile}"] },
    "stop":   { "command": "containerctl", "args": ["stop", "adventure-${profile}"] },
    "kill":   { "command": "containerctl", "args": ["kill", "adventure-${profile}"] }
  }
}
```

## Profiles

A profile is a **launch-context overlay**: it can vary launch context but never
identity or transport. A profile may set `description`, `args`, `env`,
`unsetEnv`, `workingDir`, and its own per-hook `lifecycle` overrides. It
**cannot** change `id`, `name`, `target`, `launchMode`, `stopProcessName`,
`gabpMode`, or any bridge/runtime identity — those stay at game level. Profile
names never appear in mirrored tool names or discovery.

| Profile field | Type | Behavior |
|---------------|------|----------|
| `description` | string | Human-readable label shown in `games_show` |
| `args` | string array | **Appended** after the game-level `args` |
| `env` | object | **Overrides** matching game-level `env` keys |
| `unsetEnv` | string array | Removes keys (e.g. an inherited host value) |
| `workingDir` | absolute path | **Replaces** the game-level `workingDir` when set |
| `lifecycle` | object | Per-hook overrides (`status`/`stop`/`kill`) |

Key rules:

- **`defaultProfile` is required whenever `profiles` is non-empty**, and it
  must name a configured profile. This is enforced at config load. The
  consequence is that `games_start <id>` without a profile always works — there
  is no "you must pick a profile" error state. **Make the default your safest
  profile**, since a bare start gets it.
- Exactly one profile (or the unprofiled launch) can be active per game ID at a
  time. GABS is single-instance per game ID; running two profiles at once is
  the [separate-game-IDs pattern](#id-consolidation-checklist), not a profile
  feature.
- Profile `workingDir` (and any hook `workingDir`) must be an **absolute
  path** — this design defines no relative base.
- Games without `profiles` behave exactly as before.

Start a specific profile from the CLI:

```bash
gabs games start adventure --profile combat-test
```

## Launch Inputs

A launch input is the "start with quick-start" request, made safe by
declaration: the config author declares named, typed inputs, and callers may
supply only those names and values. Inputs bind to `args` and/or `env`; they
apply **only when explicitly supplied** (no implicit defaults) and in lexical
name order.

Every input declares a required `description` and at least one `args` or `env`
binding. `${value}` inside a binding is replaced by the supplied value.
Substitution never crosses an argv boundary and never invokes a shell.

### Types and constraints

| Type | Constraints | Notes |
|------|-------------|-------|
| `boolean` | — | Applies its bindings only when supplied as `true`; `false` equals omission. Bindings **must not** contain `${value}`. |
| `string` | `enum`, `maxLength`, `pattern` | Must use `${value}` at least once. |
| `integer` | `minimum`, `maximum` (inclusive) | Signed 64-bit; must use `${value}` at least once. |

"Typed" means bounded, not merely stringly. Exact semantics, pinned so no
agent or SDK guesses:

- **Strings** without an `enum` are still bounded: default `maxLength` is
  **1024** Unicode **code points**, declarable up to **65536**. `pattern` is
  RE2 syntax (Go `regexp`) matched against the **entire** value — full-match
  anchored (`^(?:...)$`), deliberately stricter than JSON Schema's unanchored
  convention. An invalid pattern is a config validation error.
- Supplied string values are rejected at call time for NUL bytes, invalid
  UTF-8, or length/pattern violations.
- **Integers** are signed 64-bit, decoded exactly. A floating-point
  intermediary that would round values above 2^53 is rejected. `${value}`
  substitutes the canonical base-10 form (optional leading minus, no leading
  zeros, no exponent). `minimum`/`maximum` are compared inclusively in int64.
- An optional `profiles` array restricts an input to specific profiles. Two
  inputs that could both apply to one launch may not write the same env key
  (validation error).

Examples:

```json
"launchInputs": {
  "quickStart": {
    "description": "Skip menus and load the configured quick-start path",
    "type": "boolean",
    "args": ["--quick-start"]
  },
  "seed": {
    "description": "World generation seed",
    "type": "integer",
    "minimum": 0,
    "maximum": 2147483647,
    "args": ["--seed", "${value}"]
  },
  "region": {
    "description": "Startup region label",
    "type": "string",
    "pattern": "[a-z]{2}-[a-z]+-[0-9]",
    "maxLength": 32,
    "env": { "STARTUP_REGION": "${value}" }
  }
}
```

Supply an input from the CLI:

```bash
gabs games start factory --input quickStart=true --input seed=12345
```

**Do not put secrets in launch inputs.** GABS-authored state and logs store
only salted digests of input values, never the raw values — but digests of
low-entropy values (booleans, enums, small integers) are brute-forceable by a
local reader of the runtime files, and captured process output is returned
unredacted (a wrapper running `set -x`, or a game that echoes its startup
configuration, places values into evidence). Secrets belong in the process
environment via a wrapper, not in launch inputs.

## Lifecycle Hooks

Lifecycle hooks let GABS **observe** (`status`) and **control** (`stop`,
`kill`) a workload it cannot see through a plain process handle — a container,
a remote host, a service manager. Each hook is an exact executable-plus-argv
invocation. **It is never a shell.** Pipelines, redirection, and conditional
logic belong in a wrapper script you configure as the `command`.

| Hook field | Applies to | Meaning |
|------------|------------|---------|
| `command` | all | Required. Absolute path, or a name resolved via `PATH` at launch time. No placeholders. |
| `args` | all | Argument vector. Supports `${gameId}` and `${profile}` placeholders. |
| `workingDir` | all | Absolute path (after placeholder substitution). |
| `env` | all | Environment values for the hook. Supports placeholders in values. |
| `unsetEnv` | all | Keys to remove from the hook environment. |
| `timeoutSeconds` | all | See bounds below. |
| `verifyTimeoutSeconds` | stop, kill | How long GABS polls for termination after the action succeeds. |
| `runningExitCodes` | status | Overrides the default running set (`{0}`). |
| `stoppedExitCodes` | status | Overrides the default stopped set (`{1}`). |

Placeholders `${gameId}` and `${profile}` are substituted in `args`, `env`
values, and `workingDir`. `${profile}` is valid only for games that have
profiles. Unknown placeholders are validation errors.

### Timeouts and bounds

All values are integral seconds.

| Hook / field | Min | Max | Default |
|--------------|-----|-----|---------|
| `status` `timeoutSeconds` | 1 | 60 | 5 |
| `stop` `timeoutSeconds` | 1 | 600 | 30 |
| `kill` `timeoutSeconds` | 1 | 600 | 10 |
| `verifyTimeoutSeconds` (stop/kill) | 1 | 600 | 15 |

On hook timeout, GABS terminates the hook's whole process tree (process group
on Unix, Job Object on Windows) and reports `unknown` (status) or `failure`
(stop/kill).

### Hook resolution order

Per hook slot, GABS uses the first of:

1. **Profile override** — a complete replacement of that hook, with no
   field-level merge;
2. **Game-level hook**;
3. **Built-in behavior** — the tracked PID, then the `stopProcessName`
   fallback.

### Hook environment

Hooks receive a sanitized inherited environment (inherited `GABS_*`/`GABP_*`
removed) → hook `unsetEnv` → hook `env` → `GABS_GAME_ID` and, when a profile is
selected, `GABS_PROFILE`. **Hooks never receive `GABP_TOKEN` or
`GABP_SERVER_PORT`.**

## The Exit-Code Contract

This is the contract every status hook must obey, and the single most common
source of subtle misconfiguration.

**Status hook:**

- `0` = **running**
- `1` = **stopped**
- **anything else, a timeout, or a failure to execute = `unknown`**

`unknown` is a distinct, meaningful third state. It never cleans up state and
never authorizes a start while a claim exists. GABS reports what it observed
(exit code, stderr tail, timeout) and what to do next — it does **not** treat
"cannot determine" as "stopped".

**Stop / kill hook:** `0` = success, anything else = failure.

### Status hooks must not conflate "stopped" with "cannot determine"

Many tools do exactly that. A raw `docker inspect` exits `1` **both** when the
container is absent **and** when the daemon is unreachable. Wiring `docker
inspect` directly as a status hook is the canonical misconfiguration: a daemon
hiccup reads as "stopped", which unblocks a duplicate start of a workload that
is actually still running. GABS and `doctor` call this out.

The fix is a small wrapper that **checks reachability first** and exits an
*unclassified* code (e.g. `2`) when it cannot tell — leaving `0`/`1` to mean
only running/absent. Because the default contract already maps `0` → running,
`1` → stopped, and **everything else → unknown**, this wrapper needs **no**
`runningExitCodes`/`stoppedExitCodes` at all:

```sh
#!/bin/sh
# adventure-status: canonical status-hook wrapper.
# GABS calls this with the profile name as $1.
name="adventure-$1"

# 1. Reachability first: if we cannot even ask the daemon, we do NOT know.
if ! docker info >/dev/null 2>&1; then
  exit 2            # unclassified -> GABS reads this as "unknown"
fi

# 2. Now a definite answer is possible.
if docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null | grep -q true; then
  exit 0            # running
fi
exit 1              # absent / not running
```

Wire it with defaults — no exit-code overrides:

```json
"lifecycle": {
  "status": { "command": "/opt/adventure/adventure-status", "args": ["${profile}"] }
}
```

`runningExitCodes` / `stoppedExitCodes` exist only for tools whose exit
conventions genuinely differ from `0`/`1` (for example a probe that returns `3`
for "up"). They must be **non-empty and disjoint**. Do not reach for them to
paper over a tool that cannot distinguish absent from unreachable — fix that in
the wrapper instead.

## The Idempotent-Hook Contract

**Stop and kill hooks must be idempotent and self-contained.** GABS may run a
hook again after a crash, an explicit retry, or a second stop request. (A GABS
restart does **not** replay an interrupted stop/kill: recovery records the
attempt as `interrupted` and normalizes the claim under the transition lock; a
later action runs only when you explicitly issue one — see design/07-runtime-
state.md.) A stop hook must therefore be safe to run when the workload is
**already stopped**: it should treat "nothing to stop" as success, not as an
error. `containerctl stop name` that exits
non-zero because the container is already gone will be read as a failed stop.
Prefer commands that are naturally idempotent, or wrap them so an
already-stopped workload exits `0`.

The same applies to the status hook being consulted repeatedly: it is a pure
observation and must have no side effects.

## `verifyTimeoutSeconds` for save-on-exit games

A stop or kill hook exiting `0` means "shutdown **requested**", not
"terminated". After a successful action GABS runs the liveness rule for
`verifyTimeoutSeconds` (default **15**) to confirm the workload actually went
away before it clears the claim.

**Games that flush saves on shutdown need more than the default.** A game that
writes a large save on exit can stay alive for many seconds after it accepts
the stop signal. If `verifyTimeoutSeconds` is too low, GABS reports
`termination_unverified` (it will not falsely report "stopped" — unknown never
clears state), and a later observation clears the claim once the game finishes.
Raise `verifyTimeoutSeconds` to comfortably exceed the game's worst-case
save-and-quit time:

```json
"lifecycle": {
  "stop": { "command": "containerctl", "args": ["stop", "factory-${profile}"], "verifyTimeoutSeconds": 120 }
}
```

## The Windows Script-Hook Rule

On Windows, GABS **never implicitly wraps a command in a shell**. Script hooks
(`.bat`, `.cmd`, `.ps1`, `.vbs`, `.js`) are rejected if configured directly as
`command`, because implicit `cmd.exe` wrapping brings injectable argv-quoting
surprises. Invoke the interpreter explicitly and pass the script through its
args:

```json
"lifecycle": {
  "status": {
    "command": "cmd.exe",
    "args": ["/c", "C:\\gabs\\status.cmd", "${profile}"]
  }
}
```

For PowerShell, use `powershell.exe` (or `pwsh.exe`) with `-File`. The rule is:
`command` is always a real executable; the script is an argument.

## URL Launch Modes and Their Limits

`SteamAppId` and `EpicAppId` launch by handing a URL to the OS opener. That
opener owns the process it starts, so these modes cannot deliver argv,
environment, or a working directory to the game. Consequently:

- `profiles`, `launchInputs`, game-level `env`, and game-level `unsetEnv` are
  **rejected as config errors** on these modes (they would reach only the URL
  helper, never the game — silent acceptance would be a lie). The config error
  names the exact JSON path and points you at `SteamManaged`, `DirectPath`, or
  `CustomCommand`.
- Pre-existing launcher-only fields (`args`, `workingDir`) still load
  unchanged for compatibility, but `doctor` reports them as launcher-only.
- **Lifecycle hooks are valid for every mode**, including URL modes. In fact,
  for URL modes a game-level `status` hook plus a `stop` **or** `kill` hook
  *substitutes* for `stopProcessName`: hooks are stronger evidence than a name
  match, and the URL-opener helper PID proves nothing about the workload. One
  of the two mechanisms (a `stopProcessName`, or the status + stop/kill hook
  pair) remains mandatory for URL modes.

## Steam Re-exec Caveat and Workarounds

Even with `SteamManaged` (which delivers context to the process GABS spawns),
Steam may **re-exec** the game through its own client, starting the real game
process with Steam's environment and options instead of the ones GABS
injected. When that happens the bridge environment (`GABP_*`) and your profile
context can be dropped, and the game-side bridge cannot attach.

This is no longer silent: GABS's [delivery verification](#does-my-context-actually-reach-the-game)
shows which channel was lost, and `games_status` reports
`process-bridge-environment-missing`. GABS cannot fix a re-exec, but you can
work around it:

1. **`steam_appid.txt`** — placing the app ID in a `steam_appid.txt` beside the
   executable lets the game start through Steamworks directly, avoiding the
   client re-exec. GABS prepares this automatically for `SteamManaged` when it
   detects direct Steamworks startup is needed; if the game still re-execs,
   confirm the file is present in the executable's directory.
2. **Steam launch options / `%command%`** — put your arguments and a wrapper in
   Steam's per-game launch options using the `%command%` token
   (`your-wrapper %command% --extra-arg`). Steam then runs the real executable
   under your wrapper, which can re-establish the GABP environment.
3. **A wrapper via `DirectPath` / `CustomCommand`** — point GABS at a wrapper
   script that the final game process actually inherits from, sidestepping the
   client entirely. This is the most reliable option when a title insists on
   re-execing.

## Does My Context Actually Reach the Game?

GABS guarantees delivery of argv, environment, and working directory to the
**first** process it creates. Every hop after that (script → game, wrapper →
container) is owned by whoever creates it. If a game-side GABP bridge is
present, its session handshake can optionally report the raw values it observed
inside the game process; GABS compares them per channel and reports a
`contextDelivery` verdict in `games_status` (`verified` / `partial` /
`unknown`). A wrapper that forwards the managed variables but drops your
profile's `CONTENT_SET`, or changes the working directory, yields `partial` —
never a false `verified`. For the wrapper contract and the
`GABS_FORWARD_ENV` / `GABS_ABSENT_ENV` forwarding lists, see
[INTEGRATION.md](INTEGRATION.md).

---

# How Layers Combine (Resolver Order)

When GABS starts a game it resolves one immutable launch spec from four
inputs: the pinned config snapshot, the game ID, the optional profile, and the
supplied launch inputs. The rules are exact. **Args append, env keys override,
working directory replaces** — do not conflate these.

**Profile selection:**

1. An explicit `--profile` if given (an unknown name is `profile_not_found`
   with sorted candidates).
2. Otherwise `defaultProfile`.
3. A game without profiles rejects a requested profile with
   `profiles_not_configured`.

**Arguments (concatenated, in this order):**

```
game args  →  profile args  →  supplied input arg groups (lexical input-name order)
```

**Environment (later layer wins on a key collision):**

```
inherited environment (inherited GABS_*/GABP_* keys stripped)
  → game unsetEnv, then game env
    → profile unsetEnv, then profile env
      → supplied input env bindings (lexical input-name order)
        → GABS-managed variables   ← always last; callers can never override these
```

The GABS-managed variables are `GABS_GAME_ID`, `GABP_SERVER_PORT`,
`GABP_TOKEN`, `GABS_BRIDGE_PATH`, `GABS_PROFILE` (only when a profile is
selected), plus any platform/launch-mode requirements (e.g. Windows
`SystemRoot`). Because they apply last, no config layer or launch input can
displace them. On Windows, environment keys are compared case-insensitively.

`unsetEnv` exists because isolation requires *absence*, not just replacement:
an inherited host `CONTENT_SET` must be removable from a profile, and an empty
string is observably different from an unset variable.

**Working directory:** the profile `workingDir` if set, otherwise the game
`workingDir`. Paths are literal — no variable expansion.

The resolver is the single place launch context is computed; the MCP surface
and the CLI both call it, so `games_start` from either produces identical
launches.

---

# Migration Recipes

## Legacy → Profile Conversion Recipe

You have a plain game entry that already works, and you want to add an isolated
second launch context without losing what you have.

Start from:

```json
"mygame": {
  "id": "mygame",
  "name": "My Game",
  "launchMode": "DirectPath",
  "target": "/opt/mygame/start.sh",
  "args": ["--data-root", "/srv/mygame/main"],
  "workingDir": "/opt/mygame"
}
```

1. **Introduce a profile that reproduces the current launch verbatim**, and
   move the context-varying part into it. Keep the invariant parts (`target`,
   `launchMode`, base `args`) at game level:

   ```json
   "mygame": {
     "id": "mygame",
     "name": "My Game",
     "launchMode": "DirectPath",
     "target": "/opt/mygame/start.sh",
     "workingDir": "/opt/mygame",
     "defaultProfile": "main",
     "profiles": {
       "main": { "description": "Original data root", "args": ["--data-root", "/srv/mygame/main"] }
     }
   }
   ```

2. **Verify with `gabs games show mygame`** — the state, revision, and any
   warnings are shown immediately (edits are hot).
3. **Add the second profile** once `main` behaves as before:

   ```json
   "profiles": {
     "main":  { "description": "Original data root", "args": ["--data-root", "/srv/mygame/main"] },
     "arena": { "description": "Isolated arena data", "args": ["--data-root", "/srv/mygame/arena"] }
   }
   ```

4. Make `defaultProfile` your **safest** profile, since a bare
   `games_start mygame` uses it.

**Important: any edit of a proven context resets its track record.** GABS keeps
a per-context success history (starts verified, bridge connections, verified
deliveries, clean stops), keyed by a hash of the *resolved* launch context.
Because moving `--data-root` from game level into the `main` profile changes
that context's resolved hash, `main` starts again at "never proven" — even
though it launches the identical command. Adding a *new* profile (`arena`)
never touches other profiles' proof; only the context you actually changed
resets. Do the conversion deliberately, then re-establish proof with a couple
of real starts. GABS never edits proof into existence and never restores it
automatically (`gabs games doctor --show-last-good <id>` can print the last-
known-good entry for a human to compare).

## ID-Consolidation Checklist

If you currently have several game IDs that are really the same game with
different launch contexts (`mygame-main`, `mygame-arena`, `mygame-test`), you
can collapse them into one game with profiles. **Do this only when the
`target` and `launchMode` match across the IDs** — profiles cannot change
either. Before you consolidate, understand what changes:

- **The tool namespace changes.** Mirrored game-tool names follow the game ID.
  After consolidation, tools that were `mygame-arena_<tool>` become
  `mygame_<tool>` (namespace follows the surviving ID). Anything discovering
  tools by the old prefixes must be updated.
- **Scripts referencing the old IDs break.** Any script or agent instruction
  that calls `games_start mygame-arena` (or stop/status/connect on the old ID)
  must be repointed to `mygame --profile arena`. GABS does not rewrite these
  for you.
- **You lose per-ID concurrency.** Formerly independent IDs could each run at
  the same time — three IDs meant up to three concurrent instances. GABS is
  single-instance per game ID: profiles under one ID are mutually exclusive
  (exactly one profile active at a time). If you genuinely need two contexts
  running **concurrently**, keep them as separate game IDs — that is the
  supported concurrency pattern, not a profile.

GABS does not automate consolidation; it only warns about these consequences.
Weigh the cleaner discovery surface against the loss of concurrency and the
script churn before collapsing IDs.

## Old-Binary Warning

The launch-profile fields (`profiles`, `launchInputs`, `lifecycle`, game-level
`env`/`unsetEnv`, `defaultProfile`) require a GABS binary new enough to
understand them. An **older GABS binary silently ignores unknown config
fields** — it will load a profile-enabled config without error and launch the
**bare** game, dropping every profile, input, env layer, and lifecycle hook.
Nothing warns you at the game level, because to an old binary these are just
unrecognized keys.

Before relying on a profiled config, confirm your GABS binary is current
(`gabs --version`) on every machine that reads the config. A profile-enabled
config must not be used with a pre-profile binary. Some validation rules
(duplicate-member rejection, unknown-key errors inside the new subtrees) also
only exist in current binaries.

---

# Validation Rules (Quick Reference)

Config validation reports exact JSON paths. The main rules:

- Profile and input names match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`
  (case-sensitive).
- `defaultProfile` is required when `profiles` is non-empty and must name an
  existing profile. An input's `profiles` list must reference existing
  profiles.
- Config-declared env keys (game, profile, input, hook — `env` and `unsetEnv`
  alike) must match the portable grammar `^[A-Za-z_][A-Za-z0-9_]*$`. Keys with
  the reserved prefixes `GABS_`/`GABP_` (case-insensitive) are rejected
  everywhere. NUL in a value is rejected. On Windows, keys colliding after case
  folding are rejected. A key may not appear in both `env` and `unsetEnv` of the
  same layer.
- `workingDir` in profiles and hooks must be an **absolute** path (after
  placeholder substitution).
- Two launch inputs that could both apply to one launch may not write the same
  env key.
- `SteamAppId` / `EpicAppId` reject `profiles`, `launchInputs`, `env`, and
  `unsetEnv` (see [URL launch modes](#url-launch-modes-and-their-limits)).
- **Duplicate JSON object members anywhere in the file are an error** with the
  path. Both struct and map decoding silently keep the last value, so a
  duplicated profile name, hook field, or env key would otherwise launch a
  different context than the one you read.
- Unknown keys inside the new subtrees (`profiles`, `launchInputs`,
  `lifecycle`) are **errors**; unknown keys elsewhere are **warnings** naming
  the path. Warnings appear in `games_show`, in `games_list` structured
  content, and in `doctor`. Warnings with no owning game appear in a global
  `configWarnings` array.

---

# GABP Communication Reference

This section is mainly useful if you are writing or debugging a game-side
bridge. GABS uses local-only GABP communication.

When you start a game, GABS passes GABP connection data to the game-side bridge
through **environment variables**:

- `GABP_SERVER_PORT`
- `GABP_TOKEN`
- `GABS_GAME_ID`
- `GABS_PROFILE` (only when a profile is selected)

Game integrations should read these environment variables directly.

- GABS connects to game integrations on localhost (`127.0.0.1`) only.
- Each game gets a unique port and per-launch token. A connection presenting a
  previous launch's token is rejected as `stale_bridge_credential`.
- GABS may write `~/.gabs/{gameId}/bridge.json` as an endpoint cache/debug
  artifact. It additionally records the selected profile, config revision, and
  start time — **for diagnostics only.** The live bridge contract is env-only:
  a bridge must take its endpoint, token, and context from its process
  environment, never from `bridge.json`, because a file cannot prove freshness.
  A game-side bridge must not read `bridge.json` as runtime configuration or as
  a fallback for missing `GABP_*` values.

For the wrapper contract (forward argv, preserve/map env, the
`GABS_FORWARD_ENV` loop for containers, and not re-introducing
`GABS_ABSENT_ENV` names) and the optional session-welcome `observed` field
spec for bridge implementers, see [INTEGRATION.md](INTEGRATION.md).

## Shared Runtime Ownership

When a game is already starting or running, GABS writes a per-game
`runtime.json` file so other live GABS sessions can see whether the game
currently has an active owner.

- A second `games_start` returns immediately with "already starting" or
  "already running" instead of launching a second copy.
- `games_connect` takes ownership naturally once the previous owner's lease is
  idle, and returns an active-owner result while another session is still
  inside its lease.
- Game-bound tool calls also check the lease before touching the bridge.
- `games_status` reports the runtime owner, lease expiry, and bridge
  diagnostics. With no argument, `games_status` unions configured entries with
  persisted runtime claims, so a launch whose config entry was edited away is
  still findable (`configured: false`) and stoppable.

If you intentionally want a different GABS session to take over before the
active lease expires, use `games_connect` with `forceTakeover: true`.

If `games_start` reports `endpoint_cache_in_use`, the cached port is already
listening. Use `games_connect` if an already-running bridge owns that endpoint,
or `games_start` with `resetEndpoint: true` only after confirming the cached
endpoint should be rotated for a new process.

Game integrations should ignore `runtime.json`; it is for GABS itself.

---

# Other Settings

## Tool Normalization

GABS exposes strict-safe MCP tool names by default. This keeps `tools/list`
accepted by clients that reject dotted names. The `toolNormalization` section
supports:

- **`enableOpenAINormalization`** (boolean): enable/disable strict-safe MCP
  name normalization (default `true` when `toolNormalization` is omitted).
  Replaces dots, slashes, and other unsafe separators with underscores, and
  enforces the length limit.
- **`maxToolNameLength`** (integer): maximum length for tool names (default
  `64`).
- **`preserveOriginalName`** (boolean): store the original name in the tool
  description/metadata (default `true`).

Set `enableOpenAINormalization` to `false` only when you intentionally need the
old dotted MCP names in `tools/list`.

Example transformations: `games.call_tool` → `games_call_tool`;
`factory.inventory.get` → `factory_inventory_get`; GABP `core/ping` for game
`adventure` → `adventure_core_ping`. Call aliases stay backward compatible
(dotted, slash, and strict-safe forms all resolve).

For complete details, see the
[Tool Normalization Guide](OPENAI_TOOL_NORMALIZATION.md).

## Output Schema Stripping

Some MCP clients reject `tools/list` responses when a public tool includes an
`outputSchema` field. Set `stripOutputSchema` to `true` to omit output schemas
from the public tool list (default `false`):

```json
{ "stripOutputSchema": true }
```

This does not change tool execution and does not remove input schemas. Detailed
tool metadata, including output schema information, remains available through
`games_tool_detail`.

## Startup Timeouts

If your game takes longer to appear in the process list, or its GABP bridge
takes longer to start listening, override the startup waits in
`~/.gabs/config.json`.

`timeouts.startup`:

- **`processStartSeconds`** (integer): how long GABS waits for the launched
  process to become detectable in the OS process list (default `10`).
- **`gabpConnectSeconds`** (integer): the total connection budget for the
  game's GABP server to become available (default `60`). `games_start` waits
  only for a bounded initial slice, returns before MCP clients hit their own
  tool-call timeout, and continues connecting in the background. You can also
  pass a one-off `timeout` argument to `games_start` without changing the saved
  config.

`timeouts.session`:

- **`ownerLeaseSeconds`** (integer): how long an idle GABS session remains the
  active runtime owner after a normal game-bound action (default `30`). Long
  game-bound calls extend the lease to cover their requested timeout plus a
  small safety margin, so this value controls roaming between idle sessions,
  not the maximum duration of a running command.

```json
{
  "version": "1.0",
  "timeouts": {
    "startup": { "processStartSeconds": 20, "gabpConnectSeconds": 120 },
    "session": { "ownerLeaseSeconds": 30 }
  },
  "games": {}
}
```

## Stopping Games Without Hooks

When no lifecycle hooks are configured, GABS falls back to built-in stopping.
With `stopProcessName` set, GABS:

1. Finds and stops processes with that name;
2. Falls back to stopping the launched process if no match is found;
3. Supports graceful termination (`games_stop`) and force killing
   (`games_kill`).

Platform support: Windows uses `tasklist`/`taskkill`; macOS and Linux use
`pgrep` with standard process signals.

Common process names:

| Game | Platform | Process Name |
|------|----------|-------------|
| AdventureGame | Windows | `GameName.exe` |
| AdventureGame | macOS/Linux | `AdventureGame` |
| FactorySim (Java) | All | `java` |
| Engine-based game | Windows | `GameName.exe` |
| Steam game | All | Check the game's install directory |

For `SteamAppId` and `EpicAppId`, `stopProcessName` (or a status + stop/kill
hook) is mandatory. `SteamManaged` launches the resolved executable directly,
so `stopProcessName` is optional there.

---

# Managing Your Games

```bash
gabs games list            # all configured games and their status
gabs games show factory    # complete config + warnings + revision for one game
gabs games add factory     # interactive add
gabs games remove factory  # remove from config
gabs games doctor factory  # resolved executable, launcher-only fields, track record
gabs games repair factory  # e.g. switch an older SteamAppId to SteamManaged
```

# Troubleshooting

### "Game won't start"
1. Check that your target path or ID is correct.
2. Make sure the game is installed.
3. Run `gabs games doctor <id>`.
4. Try running the launch command manually first.

### "Config change was rejected"
The error names the exact JSON path. Common causes: a missing
`defaultProfile` when `profiles` is set, an env key with a reserved
`GABS_`/`GABP_` prefix, a relative `workingDir` in a profile or hook, a
duplicate JSON member, or an unknown key inside `profiles`/`launchInputs`/
`lifecycle`. Fix the file and re-run `gabs games show <id>`.

### "Can't connect to game-side bridge"
1. Make sure your game-side bridge supports GABP.
2. Check that it is listening on the port from `GABP_SERVER_PORT`.
3. Verify it reads `GABP_SERVER_PORT`, `GABP_TOKEN`, and `GABS_GAME_ID` from
   the environment (not from `bridge.json`).
4. Run `games_status` and inspect `diagnostics.code`, `diagnostics.message`,
   and `nextActions`.
5. If `diagnostics.code` is `process-bridge-environment-missing`, the process
   is visible but cannot be attached through its environment. For Steam
   launcher URL configs, run `gabs games repair <id>` first; if a managed
   launch still loses the environment, use `DirectPath` or `CustomCommand`, or
   a Steam-launch-options wrapper (see
   [Steam re-exec caveat](#steam-re-exec-caveat-and-workarounds)).

### "Status reports unknown / termination_unverified"
`unknown` never clears state. For a container status hook, confirm it exits an
unclassified code (e.g. `2`) when the daemon is unreachable rather than `1` —
see [the exit-code contract](#the-exit-code-contract). For a game that saves on
exit, raise `verifyTimeoutSeconds`.

### "Configuration not found"
The config file is created automatically when you add your first game. If it's
missing, run `gabs games add` to create a new one.
