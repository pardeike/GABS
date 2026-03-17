# GABS Deployment Guide

This guide explains how to deploy GABS in different scenarios, from local development to cloud-based AI systems.

> **Related guides:** [Configuration Guide](CONFIGURATION.md) for game setup, [AI Integration Guide](INTEGRATION.md) for connecting AI tools, [Advanced Usage Guide](ADVANCED_USAGE.md) for complex configurations.

## Architecture Overview

GABS uses a **configuration-first approach** where games are configured once, then controlled through MCP tools:

```
AI Agent ← MCP → GABS ← GABP → Game Mod ← Game API → Game
```

**Key Components:**
- **AI Agent**: Your AI assistant (Claude, ChatGPT, custom tools)
- **MCP**: Model Context Protocol for AI-tool communication
- **GABS**: Game Agent Bridge Server (this project)
- **GABP**: Game Agent Bridge Protocol (JSON-RPC style messaging)
- **Game Mod**: GABP compliant modification in your game

## Basic Deployment Flow

### 1. Configure Games
```bash
# Interactive game configuration (do this once per game)
gabs games add minecraft
gabs games add rimworld

# Verify configuration
gabs games list
```

### 2. Start GABS Server
```bash
# For AI assistants using stdio (Claude Desktop, etc.)
gabs server

# For web-based AI or HTTP integration
gabs server --http localhost:8080
```

### 3. AI Control
AI uses MCP tools to control games:
- `games.start {"gameId": "minecraft"}` - Start game and create GABP bridge
- `games.status {"gameId": "minecraft"}` - Check game status
- `games.stop {"gameId": "minecraft"}` - Stop game gracefully

## Configuration Modes

GABS currently uses **local-only communication** for security and simplicity:

### Local Communication (Current Implementation)
**Use Case:** AI and game on the same machine (recommended)

During game configuration:
- Bridge connections use localhost (127.0.0.1) only
- Unique ports and tokens generated automatically
- Game mods should prefer `GABP_SERVER_PORT`, `GABP_TOKEN`, and
  `GABS_GAME_ID`; `bridge.json` remains a fallback/debug path through
  `GABS_BRIDGE_PATH`
- Maximum security with no network exposure

GABS writes bridge metadata to `~/.gabs/{gameId}/bridge.json`, but current
mods should usually read the environment variables first and use that file only
as a fallback.

## Common Deployment Scenarios

### Scenario 1: Local Development

**Setup:** AI assistant, GABS, and game all on developer's machine.

```bash
# Configure game once
gabs games add rimworld

# Start GABS server
gabs server

# AI can now control the game through MCP tools
```

**Benefits:**
- Simple setup, no network configuration
- Fast, low-latency communication  
- Maximum security with localhost-only communication
- Perfect for development and production use

### Scenario 2: Remote AI Access to Local GABS

**Setup:** GABS and the game run on your local machine, while an AI client
reaches GABS over HTTP from another machine or service.

**On local machine:**
1. Configure and run GABS:
   ```bash
   gabs games add minecraft
   gabs server --http localhost:8080
   ```

2. Put the HTTP endpoint behind a reverse proxy, VPN, or controlled firewall
   rule before exposing it outside the machine or LAN

**On remote AI side:**
Configure the client to connect to your GABS HTTP endpoint.

**Benefits:**
- Powerful cloud AI capabilities
- Game runs on your gaming hardware
- GABS manages secure local communication with games

**Considerations:**
- Security: Prefer API key auth, reverse proxy auth, or VPN
- Latency: Network delay affects responsiveness  
- GABP communication between GABS and the mod remains local and secure

### Scenario 3: Multiple Game Server Management

**Setup:** GABS managing multiple game servers for AI.

```bash
# Configure multiple games
gabs games add minecraft-survival
gabs games add minecraft-creative
gabs games add rimworld-colony1
gabs games add terraria-world1

# Start GABS
gabs server --http :8080

# AI can manage all servers through single interface
```

**Benefits:**
- Centralized management of multiple games
- Each game gets secure local communication
- AI can control all games through unified interface

## Security Considerations

### Token Authentication

All GABP connections use token authentication. Tokens are:
- 64-character random hex strings
- Generated fresh for each session
- Stored in `bridge.json`
- Required for all GABP protocol messages

### Network Security

**For remote connections:**
- Use API key auth for the HTTP server when exposing GABS over a network
- Consider VPN or reverse proxy authentication for additional security
- Limit HTTP port exposure with firewall rules
- Monitor connections and implement rate limiting if needed
- Do not expose GABP ports directly; GABP is intended to stay on loopback only

**Recommended firewall rule:**
```bash
# Allow remote access only to the GABS HTTP endpoint from trusted IPs
sudo ufw allow from <trusted_ai_ip> to any port 8080
```

### Mod Security

Game mods should:
- Validate all incoming GABP messages
- Implement proper input sanitization
- Use principle of least privilege for game access
- Log security-relevant events

## Troubleshooting

### Connection Issues

1. **"failed to connect to GABP"**
   - Check that the game mod is running and listening on
     `127.0.0.1:GABP_SERVER_PORT`
   - Ensure `GABP_TOKEN` matches between GABS and the mod
   - Verify the mod is reading the environment variables GABS provides, or
     using `bridge.json` only as a fallback
   - Check game logs to confirm the mod finished starting its GABP server

2. **"failed to read bridge.json"** 
   - Ensure GABS launched or reattached the game first; GABS is the component
     that writes `bridge.json`
   - Prefer reading the environment variables that GABS injects, and use
     `~/.gabs/{gameId}/bridge.json` or `GABS_BRIDGE_PATH` only as a fallback
   - Check file permissions on config directory
   - Verify gameId matches between GABS and mod

3. **Connection timeout**
   - Check that the mod has actually started its GABP server yet
   - Verify the mod is listening on loopback and using the expected port/token
   - Adjust `--reconnectBackoff` if the mod starts slowly or retries need a
     wider window
   - If you are using HTTP mode remotely, check the HTTP path separately from
     the local GABP path

### Performance Optimization

**For low-latency scenarios:**
- Use local mode when possible
- Optimize mod's GABP message handling
- Consider message batching for bulk operations

**For high-latency scenarios:**  
- Implement client-side caching in GABS
- Use asynchronous operations where possible
- Batch related operations to reduce round trips

## Development Best Practices

### Mod Development

1. **Read bridge settings on mod startup:**
   ```csharp
   var config = ReadBridgeConfig(); // Prefer env vars, fall back to GABS_BRIDGE_PATH or ~/.gabs/{gameId}/bridge.json
   var server = new GABPServer("127.0.0.1", config.Port, config.Token);
   ```

2. **Handle reconnections gracefully:**
   - Allow GABS to reconnect over the mod lifetime
   - Clean up resources on disconnect
   - Maintain game state consistency

3. **Implement proper error handling:**
   - Validate all GABP requests
   - Return meaningful error messages
   - Log errors for debugging

### GABS Integration

1. **Use appropriate connection mode:**
   - Use stdio when the AI client runs locally
   - Use HTTP when a remote client needs to reach GABS
   - Use `games.connect` when you need to reattach to an already running game

2. **Configure proper timeouts:**
   - Match reconnect settings to mod startup behavior
   - Consider game loading times in timeout values

3. **Monitor connection health:**
   - Log connection events for debugging
   - Implement health checks if needed
   - Handle graceful disconnections

## Future Enhancements

Potential improvements being considered:

1. **Reverse Connection Mode:** GABS creates server, mod connects to it (useful for certain firewall scenarios)
2. **Proxy/Tunnel Support:** Built-in support for SSH tunnels, HTTP proxies
3. **Load Balancing:** Multiple GABS instances connecting to the same game
4. **Discovery Protocol:** Automatic discovery of available games/mods
5. **TLS Encryption:** Transport-level encryption for sensitive deployments

## Configuration Reference

### bridge.json Format

```json
{
  "port": 49234,
  "token": "a1b2c3d4e5f6...",
  "gameId": "minecraft"
}
```

### Command Line Options

| Flag | Description | Default |
|------|-------------|---------|
| `--addr` | HTTP server address used by `gabs server http` | `localhost:8080` |
| `--http` | HTTP server address (e.g., :8080, localhost:8080) | stdio only |
| `--reconnectBackoff` | GABP reconnect retry window (for example `100ms..5s`) | `100ms..5s` |
| `--configDir` | Override config directory | Platform-specific |
| `--log-level` | Log level: trace\|debug\|info\|warn\|error | info |
| `--grace` | Graceful stop timeout before kill | 3s |

### Environment Variables

**GABS Configuration:**
- `GABS_CONFIG_DIR`: Override default config directory  
- `GABS_LOG_LEVEL`: Set default log level

**GABP Bridge (Set by GABS for Game Mods):**
- `GABS_GAME_ID`: Game identifier passed to mod
- `GABS_BRIDGE_PATH`: Path to bridge.json configuration file
- `GABP_SERVER_PORT`: Port number for mod to listen on
- `GABP_TOKEN`: Authentication token for GABS connection
