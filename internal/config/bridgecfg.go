package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

type BridgeJSON struct {
	Port   int    `json:"port"`
	Token  string `json:"token"`
	GameId string `json:"gameId"`
}

// WriteBridgeJSON generates a random port and token, writes bridge.json atomically to the config dir
// Returns (port, token, configPath, error)
// Each game gets its own directory, ensuring concurrent launches of different games are properly isolated.
func WriteBridgeJSON(gameID, configDir string) (int, string, string, error) {
	return WriteBridgeJSONWithConfig(gameID, configDir, nil)
}

// WriteBridgeJSONWithConfig generates a random port and token, writes bridge.json atomically to the config dir
// Returns (port, token, configPath, error)
// Each game gets its own directory, ensuring concurrent launches of different games are properly isolated.
// If gamesConfig is provided, uses custom port ranges from config; otherwise uses defaults.
func WriteBridgeJSONWithConfig(gameID, configDir string, gamesConfig *GamesConfig) (int, string, string, error) {
	// Generate available port with conflict detection using config or fallback ranges
	port, err := findAvailablePortWithConfig(gamesConfig)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to find available port: %w", err)
	}

	// Generate random 64-byte hex token
	token, err := generateToken()
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to generate token: %w", err)
	}

	// Use centralized config paths
	cp, err := NewConfigPaths(configDir)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to create config paths: %w", err)
	}

	// Ensure the game directory exists
	if err := cp.EnsureGameDir(gameID); err != nil {
		return 0, "", "", fmt.Errorf("failed to create game config dir: %w", err)
	}

	// Create bridge config
	bridge := BridgeJSON{
		Port:   port,
		Token:  token,
		GameId: gameID,
	}

	// Get bridge config path and use atomic write
	cfgPath := cp.GetBridgeConfigPath(gameID)
	tempPath := cfgPath + ".tmp"

	data, err := json.MarshalIndent(bridge, "", "  ")
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to marshal bridge config: %w", err)
	}

	// Write bridge file atomically
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return 0, "", "", fmt.Errorf("failed to write temp config: %w", err)
	}

	if err := os.Rename(tempPath, cfgPath); err != nil {
		os.Remove(tempPath) // cleanup
		return 0, "", "", fmt.Errorf("failed to rename temp config: %w", err)
	}

	return port, token, cfgPath, nil
}

// ReadBridgeJSON reads existing bridge.json and returns connection info
// Returns (host, port, token, error) - host is always 127.0.0.1 for GABS
func ReadBridgeJSON(gameID, configDir string) (string, int, string, error) {
	cp, err := NewConfigPaths(configDir)
	if err != nil {
		return "", 0, "", fmt.Errorf("failed to create config paths: %w", err)
	}

	cfgPath := cp.GetBridgeConfigPath(gameID)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", 0, "", fmt.Errorf("failed to read bridge.json: %w", err)
	}

	var bridge BridgeJSON
	if err := json.Unmarshal(data, &bridge); err != nil {
		return "", 0, "", fmt.Errorf("failed to parse bridge.json: %w", err)
	}

	// GABS always uses localhost for communication
	host := "127.0.0.1"

	return host, bridge.Port, bridge.Token, nil
}

// GetBridgeConfigPath returns the path to the bridge.json file for a given game
func GetBridgeConfigPath(gameID string) string {
	cp, err := NewConfigPaths("")
	if err != nil {
		// Fallback - should not happen in normal operation
		return filepath.Join(os.TempDir(), gameID, "bridge.json")
	}
	return cp.GetBridgeConfigPath(gameID)
}



// generateToken creates a random 64-character hex token
func generateToken() (string, error) {
	bytes := make([]byte, 32) // 32 bytes = 64 hex chars
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// findAvailablePortWithConfig tries multiple port ranges to find an available port
// This improves compatibility with Windows 11 where the default ephemeral range (49152-65535)
// might be restricted by Hyper-V, WSL, or other system components
func findAvailablePortWithConfig(gamesConfig *GamesConfig) (int, error) {
	// Check for custom port ranges from configuration
	if gamesConfig != nil && gamesConfig.PortRanges != nil && len(gamesConfig.PortRanges.CustomRanges) > 0 {
		for _, portRange := range gamesConfig.PortRanges.CustomRanges {
			minPort, maxPort := portRange.Min, portRange.Max
			port, err := findAvailablePort(minPort, maxPort)
			if err == nil {
				return port, nil
			}
		}
	}

	// Define default port ranges to try in order of preference
	// Ranges are chosen to avoid common conflicts and work across different systems
	portRanges := [][]int{
		{49152, 65535}, // Default Windows/IANA ephemeral range
		{32768, 49151}, // Linux ephemeral range 
		{8000, 8999},   // Common HTTP alternate ports (8000-8999)
		{9000, 9999},   // Common application ports (9000-9999)
		{10000, 19999}, // Registered/dynamic range subset
		{20000, 29999}, // Registered/dynamic range subset
		{30000, 32767}, // Registered/dynamic range subset
	}

	var lastErr error
	for _, portRange := range portRanges {
		minPort, maxPort := portRange[0], portRange[1]
		port, err := findAvailablePort(minPort, maxPort)
		if err == nil {
			return port, nil
		}
		lastErr = err
	}

	// If all ranges failed, provide a helpful error message
	return 0, fmt.Errorf("no available ports found in any range - this may be due to Windows system restrictions (Hyper-V, WSL, etc.) or firewall settings. Consider: 1) Checking Windows reserved port ranges with 'netsh int ipv4 show excludedportrange protocol=tcp', 2) Disabling Hyper-V if not needed, 3) Configuring your firewall/antivirus, 4) Adding custom port ranges to your GABS config file in the 'portRanges' section. Last error: %w", lastErr)
}

// findAvailablePortWithFallback tries multiple port ranges to find an available port
// This improves compatibility with Windows 11 where the default ephemeral range (49152-65535)
// might be restricted by Hyper-V, WSL, or other system components
// DEPRECATED: Use findAvailablePortWithConfig instead
func findAvailablePortWithFallback() (int, error) {
	return findAvailablePortWithConfig(nil)
}


// findAvailablePort finds an available port in the given range
func findAvailablePort(minPort, maxPort int) (int, error) {
	// Try up to 100 random ports to avoid infinite loops
	for attempts := 0; attempts < 100; attempts++ {
		// Generate random port in range
		port := minPort + (randomInt() % (maxPort - minPort + 1))

		// Check if port is available
		if isPortAvailable(port) {
			return port, nil
		}
	}

	// If random selection failed, try sequential search
	for port := minPort; port <= maxPort; port++ {
		if isPortAvailable(port) {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", minPort, maxPort)
}

// isPortAvailable checks if a port is available by attempting to bind to it
func isPortAvailable(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// randomInt returns a non-negative pseudo-random int for port generation
func randomInt() int {
	// Use crypto/rand for secure random generation
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based seed if crypto/rand fails
		// This shouldn't happen in normal operation but provides resilience
		return int(time.Now().UnixNano() & 0x7FFFFFFF)
	}
	// Ensure result is non-negative by masking the sign bit
	result := int(bytes[0])<<24 | int(bytes[1])<<16 | int(bytes[2])<<8 | int(bytes[3])
	return result & 0x7FFFFFFF // Clear sign bit to ensure non-negative
}
