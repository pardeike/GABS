package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	// Assign port deterministically using config or fallback ranges
	port, err := assignPortWithConfig(gamesConfig)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to assign port: %w", err)
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

// assignPortWithConfig deterministically assigns a port from the configured ranges
// without checking availability. This avoids port availability checking issues and
// lets the actual network operations fail with clearer error messages if needed.
func assignPortWithConfig(gamesConfig *GamesConfig) (int, error) {
	// Check for custom port ranges from configuration
	if gamesConfig != nil && gamesConfig.PortRanges != nil && len(gamesConfig.PortRanges.CustomRanges) > 0 {
		for _, portRange := range gamesConfig.PortRanges.CustomRanges {
			minPort, maxPort := portRange.Min, portRange.Max
			port := assignPortFromRange(minPort, maxPort)
			return port, nil
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

	// Use the first available range (deterministic)
	minPort, maxPort := portRanges[0][0], portRanges[0][1]
	port := assignPortFromRange(minPort, maxPort)
	return port, nil
}

// findAvailablePortWithFallback is deprecated - use assignPortWithConfig instead
// DEPRECATED: Use assignPortWithConfig instead
func findAvailablePortWithFallback() (int, error) {
	return assignPortWithConfig(nil)
}


// Global port offset counter to reduce concurrent allocation collisions
var (
	portOffsetMutex sync.Mutex
	portOffset      int
)

// assignPortFromRange deterministically assigns a port from the given range
// without checking availability. This avoids port checking issues and provides
// consistent port assignment for concurrent game launches.
func assignPortFromRange(minPort, maxPort int) int {
	// Get a small offset to reduce collision probability in concurrent scenarios
	// This is deterministic but different for each call
	portOffsetMutex.Lock()
	offset := portOffset % 10  // Small offset to avoid excessive range scanning
	portOffset++
	portOffsetMutex.Unlock()

	rangeSize := maxPort - minPort + 1
	
	// Assign port deterministically with offset to avoid collisions
	port := minPort + (offset % rangeSize)
	return port
}




