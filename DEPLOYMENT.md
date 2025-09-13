# GABS Deployment Guide

This guide explains different deployment scenarios for GABS and how to configure connections between GABS, game mods, and AI systems.

## Architecture Overview

GABS uses the **Game Agent Bridge Protocol (GABP)** to communicate with game modifications. The architecture supports flexible deployment patterns to accommodate different use cases:

```
AI Agent ← MCP → GABS ← GABP → Game Mod ← Game API → Game
```

**Key Insight:** In GABP, the **game mod acts as the server** and **GABS acts as the client**. This design supports both local development and cloud-based AI scenarios.

## Connection Modes

GABS supports three connection modes via the `--gabpMode` flag:

### 1. Local Mode (Default)

**Use Case:** Local development, AI and game running on the same machine.

**How it works:**
1. GABS writes `bridge.json` with localhost configuration
2. GABS launches the game process
3. Game mod reads `bridge.json` and creates GABP server on `127.0.0.1:port`
4. GABS connects to the mod's local server
5. AI connects to GABS via MCP

```bash
# Default local mode
gabs run --gameId minecraft --launch DirectPath --target "/path/to/minecraft"

# Explicit local mode
gabs run --gameId minecraft --gabpMode local --launch DirectPath --target "/path/to/minecraft"
```

**Network Flow:**
```
AI → GABS(127.0.0.1) → Game Mod(127.0.0.1:random_port) → Game
```

### 2. Remote Mode

**Use Case:** Cloud-based AI connecting to games running on a remote machine (home computer, dedicated server).

**How it works:**
1. GABS writes `bridge.json` with remote host configuration
2. GABS launches the game process (if running on the same machine as the game)
3. Game mod reads `bridge.json` and creates GABP server on `<remote_host>:port`
4. GABS connects to the mod's remote server
5. AI (running in cloud/sandbox) connects to GABS via MCP

```bash
# GABS running on same machine as game, configured for remote AI access
gabs run --gameId minecraft --gabpMode remote --gabpHost 192.168.1.100 --launch DirectPath --target "/path/to/minecraft"

# GABS running in cloud, connecting to remote game
gabs run --gameId minecraft --gabpMode remote --gabpHost your-home-ip.ddns.net --gabpHost your-home-ip.ddns.net
```

**Network Flow:**
```
AI(cloud) → GABS(cloud) → Game Mod(home_ip:port) → Game(home)
```

### 3. Connect Mode

**Use Case:** Advanced scenarios where the game mod manages its own GABP server lifecycle.

**How it works:**
1. Game mod starts and creates its own `bridge.json` 
2. GABS reads existing `bridge.json` to get connection details
3. GABS connects to the pre-existing GABP server
4. No game process management by GABS

```bash
# Connect to existing GABP server managed by mod
gabs run --gameId modded-game --gabpMode connect

# Connect with host override
gabs attach --gameId minecraft --gabpHost 192.168.1.100
```

**Network Flow:**
```
AI → GABS → Game Mod(self-managed) → Game
```

## Common Deployment Scenarios

### Scenario 1: Local Development

**Setup:** AI assistant, GABS, and game all on developer's machine.

```bash
gabs run --gameId rimworld --launch SteamAppId --target 294100
```

**Benefits:**
- Simple setup
- No network configuration required
- Fast, low-latency communication

### Scenario 2: Cloud AI + Home Gaming

**Setup:** AI running in Claude Desktop/cloud, game on home computer.

**On home computer:**
```bash
# Configure game to accept connections from external GABS
gabs run --gameId minecraft --gabpMode remote --gabpHost 192.168.1.100 --launch DirectPath --target "/path/to/minecraft"
```

**Network requirements:**
- Port forwarding on home router for the GABP port
- Firewall rules to allow incoming connections
- Static IP or DDNS for reliable access

**In Claude Desktop MCP config:**
```json
{
  "mcpServers": {
    "gabs-minecraft": {
      "command": "gabs",
      "args": ["attach", "--gameId", "minecraft", "--gabpHost", "your-home-ip.ddns.net"]
    }
  }
}
```

**Benefits:**
- Powerful cloud AI capabilities
- Game runs on dedicated hardware
- AI can access game even when not at home computer

**Considerations:**
- Security: Use strong tokens, consider VPN
- Latency: Network delay affects responsiveness
- Reliability: Depends on home internet connection

### Scenario 3: Cloud Gaming + Cloud AI

**Setup:** Both game and AI running in cloud infrastructure.

```bash
# On cloud gaming instance
gabs run --gameId game --gabpMode remote --gabpHost <cloud_internal_ip> --launch DirectPath --target "/path/to/game"

# GABS connecting from AI instance  
gabs attach --gameId game --gabpHost <cloud_internal_ip>
```

**Benefits:**
- High performance, low latency
- Professional infrastructure
- Scalable to multiple games/AI instances

### Scenario 4: Sandbox AI Development

**Setup:** AI development in restricted sandbox, game running locally.

**Challenge:** Sandboxes typically cannot accept incoming connections.

**Solution:** Use outbound-only connection from sandbox:

```bash
# On local machine: start game and wait for connections
gabs run --gameId testgame --gabpMode remote --gabpHost 0.0.0.0 --launch DirectPath --target "/path/to/game"

# In sandbox: connect outbound to local machine
gabs attach --gameId testgame --gabpHost <local_machine_ip>
```

**Benefits:**
- Works within sandbox security constraints
- AI can still control local games
- No incoming firewall rules needed for sandbox

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
   var config = ReadBridgeConfig(); // Read from standard GAB config location
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