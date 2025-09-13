package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type BridgeJSON struct {
	Port    int    `json:"port"`
	Token   string `json:"token"`
	GameId  string `json:"gameId"`
	Agent   string `json:"agentName"`
	Host    string `json:"host,omitempty"`    // Host address for GABP server (defaults to 127.0.0.1)
	Mode    string `json:"mode,omitempty"`    // Connection mode: "local" (default) or "remote"  
	// PROMPT: Optional extra fields for mod consumption.
}

// BridgeConfig contains configuration for GABP connection
type BridgeConfig struct {
	Host string // Host for GABP server connection (defaults to "127.0.0.1")
	Mode string // Connection mode: "local" or "remote"
}

// WriteBridgeJSON generates a random port and token, writes bridge.json atomically to the config dir  
// Returns (port, token, configPath, error)
func WriteBridgeJSON(gameID, configDir string) (int, string, string, error) {
	return WriteBridgeJSONWithConfig(gameID, configDir, BridgeConfig{})
}

// WriteBridgeJSONWithConfig generates bridge.json with custom configuration
// Returns (port, token, configPath, error)
func WriteBridgeJSONWithConfig(gameID, configDir string, config BridgeConfig) (int, string, string, error) {
	// Generate random port (49152-65535 dynamic range)
	port := 49152 + (randomInt() % (65535 - 49152 + 1))
	
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

	// Set defaults for configuration
	host := config.Host
	if host == "" {
		host = "127.0.0.1" // Default to localhost
	}
	mode := config.Mode
	if mode == "" {
		mode = "local" // Default to local mode
	}

	// Create bridge config
	bridge := BridgeJSON{
		Port:   port,
		Token:  token,
		GameId: gameID,
		Agent:  "gabs-v0.1.0",
		Host:   host,
		Mode:   mode,
	}

	// Write atomically (temp file + rename)
	cfgPath := filepath.Join(cfgDir, "bridge.json")
	tempPath := cfgPath + ".tmp"

	data, err := json.MarshalIndent(bridge, "", "  ")
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to marshal bridge config: %w", err)
	}

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
// Returns (host, port, token, error) 
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

	host := bridge.Host
	if host == "" {
		host = "127.0.0.1" // Default to localhost for backward compatibility
	}

	return host, bridge.Port, bridge.Token, nil
}

// getConfigDir computes per-OS config directory
func getConfigDir(gameID, override string) (string, error) {
	if override != "" {
		return override, nil
	}

	var baseDir string
	switch runtime.GOOS {
	case "windows":
		// %APPDATA%\GAB\<gameId>\
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA environment variable not set")
		}
		baseDir = filepath.Join(appData, "GAB")
	case "darwin":
		// ~/Library/Application Support/GAB/<gameId>/
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		baseDir = filepath.Join(homeDir, "Library", "Application Support", "GAB")
	default:
		// Linux: $XDG_STATE_HOME/gab/<gameId>/ or ~/.local/state/gab/<gameId>/
		stateHome := os.Getenv("XDG_STATE_HOME")
		if stateHome == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("failed to get home directory: %w", err)
			}
			stateHome = filepath.Join(homeDir, ".local", "state")
		}
		baseDir = filepath.Join(stateHome, "gab")
	}

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

// randomInt returns a pseudo-random int for port generation
func randomInt() int {
	// Simple random number for port - not cryptographically secure but sufficient
	bytes := make([]byte, 4)
	rand.Read(bytes)
	return int(bytes[0])<<24 | int(bytes[1])<<16 | int(bytes[2])<<8 | int(bytes[3])
}
