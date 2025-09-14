# Advanced Usage Guide

This guide covers advanced GABS features for power users and complex setups.

## Multiple Game Instances

You can run multiple copies of the same game with different configurations:

```bash
# Configure multiple Minecraft servers
gabs games add minecraft-survival
gabs games add minecraft-creative
gabs games add minecraft-test

# Start GABS server
gabs server
```

AI can then control each instance separately:
- "Start the creative server but keep survival server running"
- "Check status of all minecraft instances"
- "Stop the test server and start survival"

### Use Cases
- **Testing**: Run test servers alongside production
- **Multiple worlds**: Different game modes or worlds
- **Load balancing**: Switch between servers based on player count

## Advanced Launch Configurations

### Custom Commands with Arguments
For complex launch setups:

```bash
gabs games add complex-server
# When prompted, choose CustomCommand and enter:
# java -Xmx8G -Xms4G -jar server.jar --world=survival --port=25565 --nogui
```

### Environment Variables
Set environment variables in custom commands:

```bash
# Custom command example:
# export JAVA_HOME=/usr/lib/jvm/java-17 && java -jar server.jar
```

### Batch Scripts and Shell Scripts
Point to scripts that handle complex startup logic:

```bash
# DirectPath pointing to a script:
# /home/user/game-scripts/start-minecraft.sh
```

## HTTP Mode Deep Dive

GABS can run as an HTTP server for integration with web tools and services.

### Starting HTTP Mode
```bash
# Local only
gabs server --http localhost:8080

# All interfaces (careful with security!)
gabs server --http :8080

# Specific interface
gabs server --http 192.168.1.100:8080
```

### API Endpoints

#### MCP Protocol Endpoint
```
POST /mcp
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "games_list",
    "arguments": {}
  }
}
```

#### Health Check
```
GET /health
```
Returns server status and configured games.

### Integration Examples

#### curl Commands
```bash
# List games
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"games_list","arguments":{}}}'

# Start a game
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"games_start","arguments":{"gameId":"minecraft"}}}'
```

#### Python Integration
```python
import requests
import json

class GABSClient:
    def __init__(self, base_url="http://localhost:8080"):
        self.base_url = base_url
        self.session = requests.Session()
        self.session.headers.update({"Content-Type": "application/json"})
    
    def call_tool(self, tool_name, arguments=None):
        if arguments is None:
            arguments = {}
            
        payload = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "tools/call",
            "params": {
                "name": tool_name,
                "arguments": arguments
            }
        }
        
        response = self.session.post(f"{self.base_url}/mcp", json=payload)
        return response.json()
    
    def list_games(self):
        return self.call_tool("games_list")
    
    def start_game(self, game_id):
        return self.call_tool("games_start", {"gameId": game_id})
    
    def stop_game(self, game_id):
        return self.call_tool("games_stop", {"gameId": game_id})

# Usage
client = GABSClient()
games = client.list_games()
print("Available games:", games)

result = client.start_game("minecraft")
print("Start result:", result)
```

#### JavaScript/Node.js Integration
```javascript
const axios = require('axios');

class GABSClient {
    constructor(baseUrl = 'http://localhost:8080') {
        this.baseUrl = baseUrl;
        this.requestId = 1;
    }
    
    async callTool(toolName, arguments = {}) {
        const payload = {
            jsonrpc: '2.0',
            id: this.requestId++,
            method: 'tools/call',
            params: {
                name: toolName,
                arguments: arguments
            }
        };
        
        try {
            const response = await axios.post(`${this.baseUrl}/mcp`, payload);
            return response.data;
        } catch (error) {
            console.error('GABS API error:', error.response?.data || error.message);
            throw error;
        }
    }
    
    async listGames() {
        return await this.callTool('games_list');
    }
    
    async startGame(gameId) {
        return await this.callTool('games_start', { gameId });
    }
    
    async stopGame(gameId) {
        return await this.callTool('games_stop', { gameId });
    }
    
    async getGameStatus(gameId) {
        return await this.callTool('games_status', { gameId });
    }
}

// Usage
const client = new GABSClient();

async function manageGames() {
    try {
        const games = await client.listGames();
        console.log('Available games:', games);
        
        await client.startGame('minecraft');
        console.log('Started minecraft');
        
        const status = await client.getGameStatus('minecraft');
        console.log('Minecraft status:', status);
        
    } catch (error) {
        console.error('Error managing games:', error);
    }
}

manageGames();
```

## Configuration File Advanced Features

### Multiple Config Files
You can use different config files for different setups:

```bash
# Use custom config location
export GABS_CONFIG="/path/to/custom/config.json"
gabs server

# Or specify directly
gabs --config "/path/to/custom/config.json" server
```

### Config Validation
Validate your configuration:

```bash
gabs config validate
```

### Config Export/Import
```bash
# Export configuration
gabs config export > my-games.json

# Import configuration
gabs config import < my-games.json
```

## Security Considerations

### Local vs Remote Access
- **Local only** (default): GABS only accepts connections from localhost
- **Remote access**: Use `--http :8080` carefully, consider firewall rules

### Authentication
- GABP connections use token authentication automatically
- HTTP mode currently has no authentication (use reverse proxy if needed)

### Network Security
For remote deployments:
```bash
# Use a reverse proxy with authentication
nginx -> GABS HTTP mode
```

Example nginx config:
```nginx
location /gabs/ {
    auth_basic "GABS Access";
    auth_basic_user_file /etc/nginx/.htpasswd;
    proxy_pass http://localhost:8080/;
}
```

## Performance Tuning

### Resource Limits
GABS is lightweight, but for many games consider:

```bash
# Linux: Set process limits
ulimit -n 4096  # File descriptors
ulimit -u 2048  # Process count

# Then start GABS
gabs server
```

### Monitoring
Monitor GABS performance:

```bash
# Check process stats
ps aux | grep gabs

# Check network connections
netstat -tulpn | grep gabs

# Check log files (if configured)
tail -f ~/.gabs/logs/gabs.log
```

## Scripting and Automation

### Bash Scripts
```bash
#!/bin/bash
# start-game-servers.sh

# Start GABS
gabs server --http :8080 &
GABS_PID=$!

# Wait for GABS to start
sleep 5

# Start games via API
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"games_start","arguments":{"gameId":"minecraft"}}}'

curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"games_start","arguments":{"gameId":"rimworld"}}}'

echo "Game servers started. GABS PID: $GABS_PID"
```

### Systemd Service (Linux)
```ini
# /etc/systemd/system/gabs.service
[Unit]
Description=GABS Game Server Manager
After=network.target

[Service]
Type=simple
User=gameserver
WorkingDirectory=/home/gameserver
ExecStart=/usr/local/bin/gabs server --http :8080
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl enable gabs
sudo systemctl start gabs
```

## Troubleshooting Advanced Issues

### Port Conflicts
```bash
# Check what's using a port
lsof -i :8080
netstat -tulpn | grep :8080

# Use a different port
gabs server --http :8081
```

### Memory Issues
```bash
# Monitor GABS memory usage
top -p $(pgrep gabs)

# Check game process memory
ps aux | grep -E "(minecraft|rimworld|gabs)"
```

### Network Issues
```bash
# Test connectivity
telnet localhost 8080

# Check firewall (Linux)
sudo iptables -L | grep 8080

# Check firewall (macOS)
sudo pfctl -s rules | grep 8080
```

### Debug Mode
```bash
# Run with verbose logging
GABS_DEBUG=1 gabs server

# Or with custom log level
GABS_LOG_LEVEL=debug gabs server
```

## Integration with CI/CD

### Automated Testing
```yaml
# GitHub Actions example
name: Game Server Tests
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v2
      
      - name: Install GABS
        run: |
          wget https://github.com/pardeike/GABS/releases/latest/download/gabs-linux-amd64
          chmod +x gabs-linux-amd64
          sudo mv gabs-linux-amd64 /usr/local/bin/gabs
      
      - name: Configure test game
        run: |
          gabs games add test-game
          # Configure with test parameters
      
      - name: Start GABS
        run: |
          gabs server --http :8080 &
          sleep 5
      
      - name: Test game management
        run: |
          # Test API endpoints
          curl -X POST http://localhost:8080/mcp \
            -H "Content-Type: application/json" \
            -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"games_list","arguments":{}}}'
```

This advanced guide covers the more complex features of GABS. For basic usage, start with the main README and other guides first.