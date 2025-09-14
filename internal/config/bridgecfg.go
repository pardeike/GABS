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
	Agent  string `json:"agentName"`
	Host   string `json:"host,omitempty"` // Always 127.0.0.1 for local communication
	Mode   string `json:"mode,omitempty"` // Always "local" for GABS
	// PROMPT: Optional extra fields for mod consumption.
}

// BridgeConfig contains configuration for GABP connection (simplified for local-only use)
type BridgeConfig struct {
	// Reserved for future extensions - currently GABS only supports local communication
}

// WriteBridgeJSON generates a random port and token, writes bridge.json atomically to the config dir
// Returns (port, token, configPath, error)
func WriteBridgeJSON(gameID, configDir string) (int, string, string, error) {
	return WriteBridgeJSONWithConfig(gameID, configDir, BridgeConfig{})
}

// WriteBridgeJSONWithConfig generates bridge.json (simplified for local-only use)
// Returns (port, token, configPath, error)
func WriteBridgeJSONWithConfig(gameID, configDir string, config BridgeConfig) (int, string, string, error) {
	// Generate available port with conflict detection
	port, err := findAvailablePort(49152, 65535)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to find available port: %w", err)
	}

	// Generate random 64-byte hex token
	token, err := generateToken()
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to generate token: %w", err)
	}

	// Determine config directory
	cfgDir, err := getConfigDir(gameID, configDir)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to get config dir: %w", err)
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return 0, "", "", fmt.Errorf("failed to create config dir: %w", err)
	}

	// GABS always communicates locally
	host := "127.0.0.1"
	mode := "local"

	// Create bridge config
	bridge := BridgeJSON{
		Port:   port,
		Token:  token,
		GameId: gameID,
		Agent:  "gabs-v0.1.0",
		Host:   host,
		Mode:   mode,
	}

	// Create unique filename with timestamp to avoid conflicts in concurrent launches
	timestamp := time.Now().UnixNano()
	cfgFilename := fmt.Sprintf("bridge-%d.json", timestamp)
	cfgPath := filepath.Join(cfgDir, cfgFilename)
	tempPath := cfgPath + ".tmp"

	// Also create/update the standard bridge.json for backward compatibility
	standardPath := filepath.Join(cfgDir, "bridge.json")

	data, err := json.MarshalIndent(bridge, "", "  ")
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to marshal bridge config: %w", err)
	}

	// Write unique bridge file atomically
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return 0, "", "", fmt.Errorf("failed to write temp config: %w", err)
	}

	if err := os.Rename(tempPath, cfgPath); err != nil {
		os.Remove(tempPath) // cleanup
		return 0, "", "", fmt.Errorf("failed to rename temp config: %w", err)
	}

	// Also update standard bridge.json for backward compatibility
	tempStandardPath := standardPath + ".tmp"
	if err := os.WriteFile(tempStandardPath, data, 0644); err != nil {
		// Don't fail if we can't write the standard file - the unique one is the important one
		os.Remove(tempStandardPath)
	} else if err := os.Rename(tempStandardPath, standardPath); err != nil {
		os.Remove(tempStandardPath)
	}

	return port, token, cfgPath, nil
}

// ReadBridgeJSON reads existing bridge.json and returns connection info
// Returns (host, port, token, error) - host is always 127.0.0.1 for GABS
func ReadBridgeJSON(gameID, configDir string) (string, int, string, error) {
	cfgDir, err := getConfigDir(gameID, configDir)
	if err != nil {
		return "", 0, "", fmt.Errorf("failed to get config dir: %w", err)
	}

	cfgPath := filepath.Join(cfgDir, "bridge.json")
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

// getConfigDir computes per-OS config directory
func getConfigDir(gameID, override string) (string, error) {
	if override != "" {
		return override, nil
	}

	// Use ~/.gabs/ directory on all platforms as requested in the issue
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	baseDir := filepath.Join(homeDir, ".gabs")
	return filepath.Join(baseDir, gameID), nil
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

// randomInt returns a pseudo-random int for port generation
func randomInt() int {
	// Simple random number for port - not cryptographically secure but sufficient
	bytes := make([]byte, 4)
	rand.Read(bytes)
	return int(bytes[0])<<24 | int(bytes[1])<<16 | int(bytes[2])<<8 | int(bytes[3])
}
