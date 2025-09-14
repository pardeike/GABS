# GABS Deployment Guide

This guide explains how to deploy GABS in different scenarios, from local development to cloud-based AI systems.

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

When adding games with `gabs games add`, you can configure different connection patterns:

### Local Mode (Default)
**Use Case:** AI and game on the same machine

During game configuration:
- **GABP Mode**: `local`
- **GABP Host**: `127.0.0.1` (default)

Game mods will create GABP servers on localhost only.

### Remote Mode  
**Use Case:** AI in cloud, game on remote machine

During game configuration:
- **GABP Mode**: `remote`
- **GABP Host**: Your machine's IP address (e.g., `192.168.1.100`)

Game mods will create GABP servers accessible from other machines.

### Connect Mode
**Use Case:** Game mod manages its own GABP server

During game configuration:
- **GABP Mode**: `connect`

GABS will look for existing bridge configuration instead of creating one.

## Common Deployment Scenarios

### Scenario 1: Local Development

**Setup:** AI assistant, GABS, and game all on developer's machine.

```bash
# Configure game once
gabs games add rimworld
# When prompted, accept defaults (local mode)

# Start GABS server
gabs server

# AI can now control the game through MCP tools
```

**Benefits:**
- Simple setup, no network configuration
- Fast, low-latency communication  
- Perfect for development and testing

### Scenario 2: Cloud AI + Home Gaming

**Setup:** AI running in Claude Desktop/cloud, game on home computer.

**On home computer:**
1. Configure game for remote access:
   ```bash
   gabs games add minecraft
   # When prompted:
   # - GABP Mode: remote
   # - GABP Host: 192.168.1.100 (your home machine's LAN IP)
   ```

2. Start GABS server:
   ```bash
   gabs server --http 0.0.0.0:8080
   ```

3. Configure port forwarding on router for port 8080

**On cloud AI side:**
Configure AI to connect to your home GABS server via HTTP.

**Benefits:**
- Powerful cloud AI capabilities
- Game runs on your gaming hardware
- AI can access games remotely

**Considerations:**
- Security: Use firewall rules, consider VPN
- Latency: Network delay affects responsiveness  
- Reliability: Depends on home internet connection

### Scenario 3: Professional Cloud Gaming

**Setup:** Both game and AI running in cloud infrastructure.

```bash
# On cloud gaming instance
gabs games add game
# Configure with remote mode, internal cloud IP

# Start GABS server
gabs server --http 0.0.0.0:8080

# AI connects to cloud GABS instance
```

**Benefits:**
- High performance, low latency
- Professional infrastructure reliability
- Scalable to multiple games/AI instances

### Scenario 4: Multiple Game Server Management

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

## Security Considerations

### Token Authentication

All GABP connections use token authentication. Tokens are:
- 64-character random hex strings
- Generated fresh for each session
- Stored in `bridge.json`
- Required for all GABP protocol messages

### Network Security

**For remote connections:**
- Use strong, unique tokens
- Consider VPN for additional security
- Limit port exposure with firewall rules
- Monitor connections and implement rate limiting if needed

**Recommended firewall rule:**
```bash
# Allow GABP connections from specific IPs only
sudo ufw allow from <trusted_ai_ip> to any port <gabp_port>
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
   - Check if game mod is running and has created bridge.json
   - Verify host/port configuration
   - Check firewall settings
   - Ensure token matches between GABS and mod

2. **"failed to read bridge.json"** 
   - Ensure game mod has started and is GABP-compliant
   - Check that mod is reading from `~/.gabs/{gameId}/bridge.json` or using `GABS_BRIDGE_PATH` environment variable
   - Check file permissions on config directory
   - Verify gameId matches between GABS and mod

3. **Connection timeout**
   - Increase `--reconnectBackoff` for unstable networks
   - Check network connectivity between GABS and mod
   - Verify mod is accepting connections on correct interface

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

1. **Read bridge.json on mod startup:**
   ```csharp
   var config = ReadBridgeConfig(); // Read from GABS_BRIDGE_PATH env var or ~/.gabs/{gameId}/bridge.json
   var server = new GABPServer(config.Host, config.Port, config.Token);
   ```

2. **Handle reconnections gracefully:**
   - Allow multiple GABS connections over mod lifetime
   - Clean up resources on disconnect
   - Maintain game state consistency

3. **Implement proper error handling:**
   - Validate all GABP requests
   - Return meaningful error messages
   - Log errors for debugging

### GABS Integration

1. **Use appropriate connection mode:**
   - Local for development
   - Remote for production cloud AI
   - Connect for advanced mod scenarios

2. **Configure proper timeouts:**
   - Match reconnect settings to network conditions
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
  "gameId": "minecraft", 
  "agentName": "gabs-v0.1.0",
  "host": "192.168.1.100",
  "mode": "remote"
}
```

### Command Line Options

| Flag | Description | Default |
|------|-------------|---------|
| `--gabpMode` | Connection mode: local\|remote\|connect | local |
| `--gabpHost` | GABP server host address | 127.0.0.1 |
| `--reconnectBackoff` | Reconnection retry timing | 100ms..5s |
| `--configDir` | Override config directory | Platform-specific |

### Environment Variables

- `GAB_CONFIG_DIR`: Override default config directory
- `GAB_LOG_LEVEL`: Set default log level
- `GAB_GABP_HOST`: Set default GABP host