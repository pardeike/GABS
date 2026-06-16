package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
)

type BridgeJSON struct {
	Port   int    `json:"port"`
	Token  string `json:"token"`
	GameId string `json:"gameId"`
}

type BridgeEndpointInUseError struct {
	GameID     string
	Port       int
	ConfigPath string
}

func (e *BridgeEndpointInUseError) Error() string {
	return fmt.Sprintf("GABS endpoint cache for game %q uses port %d, but that port is already listening", e.GameID, e.Port)
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
	// Assign an available local port using config or fallback ranges.
	port, err := assignPortWithConfig(gamesConfig)
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to assign port: %w", err)
	}

	// Generate random 64-byte hex token
	token, err := generateToken()
	if err != nil {
		return 0, "", "", fmt.Errorf("failed to generate token: %w", err)
	}

	cfgPath, err := WriteBridgeJSONWithEndpoint(gameID, configDir, port, token)
	if err != nil {
		return 0, "", "", err
	}

	return port, token, cfgPath, nil
}

// EnsureBridgeJSONWithConfig returns an existing valid bridge.json endpoint for
// a game, or creates one if no durable endpoint exists yet.
func EnsureBridgeJSONWithConfig(gameID, configDir string, gamesConfig *GamesConfig) (int, string, string, bool, error) {
	cp, err := NewConfigPaths(configDir)
	if err != nil {
		return 0, "", "", false, fmt.Errorf("failed to create config paths: %w", err)
	}

	if err := cp.EnsureGameDir(gameID); err != nil {
		return 0, "", "", false, fmt.Errorf("failed to create game config dir: %w", err)
	}

	cfgPath := cp.GetBridgeConfigPath(gameID)
	if bridge, err := readBridgeJSONFile(cfgPath); err == nil && validBridgeEndpoint(gameID, bridge) {
		return bridge.Port, bridge.Token, cfgPath, true, nil
	}

	port, token, path, err := WriteBridgeJSONWithConfig(gameID, configDir, gamesConfig)
	if err != nil {
		return 0, "", "", false, err
	}
	return port, token, path, false, nil
}

func PrepareBridgeEndpointForStart(gameID, configDir string, gamesConfig *GamesConfig, resetEndpoint bool) (int, string, string, bool, error) {
	if resetEndpoint {
		port, token, path, err := WriteBridgeJSONWithConfig(gameID, configDir, gamesConfig)
		return port, token, path, false, err
	}

	cp, err := NewConfigPaths(configDir)
	if err != nil {
		return 0, "", "", false, fmt.Errorf("failed to create config paths: %w", err)
	}

	if err := cp.EnsureGameDir(gameID); err != nil {
		return 0, "", "", false, fmt.Errorf("failed to create game config dir: %w", err)
	}

	cfgPath := cp.GetBridgeConfigPath(gameID)
	if bridge, err := readBridgeJSONFile(cfgPath); err == nil && validBridgeEndpoint(gameID, bridge) {
		if !isPortAvailable(bridge.Port) {
			return 0, "", cfgPath, false, &BridgeEndpointInUseError{
				GameID:     gameID,
				Port:       bridge.Port,
				ConfigPath: cfgPath,
			}
		}
		return bridge.Port, bridge.Token, cfgPath, true, nil
	}

	port, token, path, err := WriteBridgeJSONWithConfig(gameID, configDir, gamesConfig)
	if err != nil {
		return 0, "", "", false, err
	}
	return port, token, path, false, nil
}

// WriteBridgeJSONWithEndpoint writes a specific bridge endpoint atomically.
func WriteBridgeJSONWithEndpoint(gameID, configDir string, port int, token string) (string, error) {
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid bridge port %d", port)
	}
	if token == "" {
		return "", fmt.Errorf("bridge token cannot be empty")
	}

	cp, err := NewConfigPaths(configDir)
	if err != nil {
		return "", fmt.Errorf("failed to create config paths: %w", err)
	}
	if err := cp.EnsureGameDir(gameID); err != nil {
		return "", fmt.Errorf("failed to create game config dir: %w", err)
	}

	bridge := BridgeJSON{
		Port:   port,
		Token:  token,
		GameId: gameID,
	}

	cfgPath := cp.GetBridgeConfigPath(gameID)
	if err := writeBridgeJSONFile(cfgPath, bridge); err != nil {
		return "", err
	}

	return cfgPath, nil
}

func readBridgeJSONFile(cfgPath string) (BridgeJSON, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return BridgeJSON{}, err
	}

	var bridge BridgeJSON
	if err := json.Unmarshal(data, &bridge); err != nil {
		return BridgeJSON{}, err
	}

	return bridge, nil
}

func writeBridgeJSONFile(cfgPath string, bridge BridgeJSON) error {
	tempPath := cfgPath + ".tmp"

	data, err := json.MarshalIndent(bridge, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal bridge config: %w", err)
	}

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}

	if err := os.Rename(tempPath, cfgPath); err != nil {
		os.Remove(tempPath) // cleanup
		return fmt.Errorf("failed to rename temp config: %w", err)
	}

	return nil
}

func validBridgeEndpoint(gameID string, bridge BridgeJSON) bool {
	if bridge.Port <= 0 || bridge.Port > 65535 || bridge.Token == "" {
		return false
	}
	return bridge.GameId == "" || bridge.GameId == gameID
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

// assignPortWithConfig assigns an available loopback port from the configured ranges.
func assignPortWithConfig(gamesConfig *GamesConfig) (int, error) {
	ranges := make([]PortRange, 0, 8)

	// Check for custom port ranges from configuration
	if gamesConfig != nil && gamesConfig.PortRanges != nil && len(gamesConfig.PortRanges.CustomRanges) > 0 {
		ranges = append(ranges, gamesConfig.PortRanges.CustomRanges...)
	} else {
		// Define default port ranges to try in order of preference.
		ranges = append(ranges,
			PortRange{Min: 49152, Max: 65535}, // Default Windows/IANA ephemeral range
			PortRange{Min: 32768, Max: 49151}, // Linux ephemeral range
			PortRange{Min: 8000, Max: 8999},   // Common HTTP alternate ports
			PortRange{Min: 9000, Max: 9999},   // Common application ports
			PortRange{Min: 10000, Max: 19999},
			PortRange{Min: 20000, Max: 29999},
			PortRange{Min: 30000, Max: 32767},
		)
	}

	var lastErr error
	for _, portRange := range ranges {
		port, err := findAvailablePortInRange(portRange.Min, portRange.Max)
		if err == nil {
			return port, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no port ranges configured")
	}
	return 0, fmt.Errorf("no available bridge port found: %w", lastErr)
}

// findAvailablePortWithFallback is deprecated - use assignPortWithConfig instead
// DEPRECATED: Use assignPortWithConfig instead
func findAvailablePortWithFallback() (int, error) {
	return assignPortWithConfig(nil)
}

// Global port offset counter to reduce concurrent allocation collisions.
var (
	portOffsetMutex sync.Mutex
	portOffset      int
)

func findAvailablePortInRange(minPort, maxPort int) (int, error) {
	if minPort <= 0 || maxPort > 65535 || minPort > maxPort {
		return 0, fmt.Errorf("invalid port range %d-%d", minPort, maxPort)
	}

	rangeSize := maxPort - minPort + 1
	offset := nextPortOffset(rangeSize)

	for i := 0; i < rangeSize; i++ {
		port := minPort + ((offset + i) % rangeSize)
		if isPortAvailable(port) {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", minPort, maxPort)
}

func nextPortOffset(rangeSize int) int {
	randomOffset := 0
	if n, err := rand.Int(rand.Reader, big.NewInt(int64(rangeSize))); err == nil {
		randomOffset = int(n.Int64())
	}

	portOffsetMutex.Lock()
	offset := (portOffset + randomOffset) % rangeSize
	portOffset++
	portOffsetMutex.Unlock()
	return offset
}

func isPortAvailable(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}
