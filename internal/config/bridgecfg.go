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

// The bridge.json read-modify-write for a game must be atomic against a
// concurrent endpoint rotation — otherwise endpoint preparation (read/reuse/
// rotate) and the spawn-boundary diagnostics stamp are three separate steps
// (read, compare, rewrite) that an interleaving rotation can defeat, restoring a
// superseded launch's token/diagnostics over the successor's rotated endpoint.
// An earlier fix used a process-local sync.Map of mutexes, which
// cannot serialize a superseded GABS process against a successor GABS process:
// endpoint rotation and the async stamp run OUTSIDE any held transition lock
// (GateStart releases its lock internally, see withBridgeLock), so the fence
// MUST cross process boundaries. The dedicated cross-process bridge.lock
// (withBridgeLock) is held across the whole read-compare-write.

// bridgeStampAfterReadHook is a test-only barrier fired inside
// StampBridgeDiagnostics after the read, while the bridge lock is held.
var bridgeStampAfterReadHook func()

type BridgeJSON struct {
	Port   int    `json:"port"`
	Token  string `json:"token"`
	GameId string `json:"gameId"`
	// Diagnostic-only fields (design/03 §"Files are diagnostic, never live
	// handoff"): the selected profile, the config revision the launch was
	// resolved from, and the launch start time — for doctor output only.
	// The live bridge contract stays env-only; nothing reads these to make a
	// liveness, attach, or attribution decision.
	Profile        string `json:"profile,omitempty"`
	ConfigRevision string `json:"configRevision,omitempty"`
	// StartedAt is the binding key name (design/20-implementation-map.md:235),
	// RFC3339. Diagnostic-only.
	StartedAt string `json:"startedAt,omitempty"`
}

// BridgeDiagnostics carries the diagnostic-only fields stamped into
// bridge.json at spawn (design/03, design/20). A zero value stamps nothing —
// the non-start writers (Ensure/WriteBridgeJSON, all test-only) pass it.
type BridgeDiagnostics struct {
	Profile        string
	ConfigRevision string
	StartedAt      string
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

// PrepareBridgeEndpointForStart allocates or reuses the endpoint (port +
// per-launch token) at endpoint-preparation time. It writes NO diagnostic
// fields — those are stamped later, at the spawn boundary, by
// StampBridgeDiagnostics (design/20: "written at spawn"). Rewriting with an
// empty BridgeDiagnostics also CLEARS any stale diagnostics a reused file
// carried, so a pre-spawn failure never leaves a profile/revision/startedAt
// for a process that was never spawned.
func PrepareBridgeEndpointForStart(gameID, configDir string, gamesConfig *GamesConfig, resetEndpoint bool) (int, string, string, bool, error) {
	// Hold the cross-process bridge lock across the entire read/reuse/rotate so
	// a concurrent stamp or preparation — in this process or a successor GABS
	// process — cannot interleave and restore a superseded endpoint.
	// A business error (e.g. port-in-use) is returned verbatim via opErr,
	// not the lock error, so callers still see the exact endpoint diagnostics.
	var port int
	var token, path string
	var reused bool
	var opErr error
	if lerr := withBridgeLock(configDir, gameID, func() error {
		port, token, path, reused, opErr = prepareBridgeEndpointForStartLocked(gameID, configDir, gamesConfig, resetEndpoint)
		return nil
	}); lerr != nil {
		return 0, "", "", false, lerr
	}
	return port, token, path, reused, opErr
}

func prepareBridgeEndpointForStartLocked(gameID, configDir string, gamesConfig *GamesConfig, resetEndpoint bool) (int, string, string, bool, error) {
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
		// The port may be reused; the token is per-launch and always
		// rotates, so a superseded process's credentials can never attach
		// to a newer claim (design/03). Writing with no diagnostics clears
		// the previous launch's stale profile/revision until the spawn
		// boundary restamps this launch's values.
		token, err := generateToken()
		if err != nil {
			return 0, "", "", false, fmt.Errorf("failed to generate token: %w", err)
		}
		if _, err := WriteBridgeJSONWithEndpoint(gameID, configDir, bridge.Port, token); err != nil {
			return 0, "", "", false, err
		}
		return bridge.Port, token, cfgPath, true, nil
	}

	port, token, path, err := WriteBridgeJSONWithConfig(gameID, configDir, gamesConfig)
	if err != nil {
		return 0, "", "", false, err
	}
	return port, token, path, false, nil
}

// StampBridgeDiagnostics rewrites bridge.json with the diagnostic-only fields
// (profile, configRevision, startedAt) at the spawn boundary (design/20),
// preserving the endpoint (port/token) written at preparation time. It is
// FENCED to this launch's endpoint generation: it refuses to
// stamp unless bridge.json still carries the expected port AND token, so a
// launch whose claim/endpoint was superseded between spawn and stamp can never
// write its profile/revision onto the successor's rotated token. Returns
// ErrBridgeEndpointRotated in that case; a missing file returns its read error.
// Diagnostics never influence a read decision.
func StampBridgeDiagnostics(gameID, configDir string, expectedPort int, expectedToken string, diag BridgeDiagnostics) error {
	// The read → compare → rewrite must be atomic with respect to any endpoint
	// rotation — in this process OR a successor GABS process — or a successor's
	// token published between the read and the write would be overwritten by
	// this stale launch. The cross-process bridge lock provides
	// that fence.
	return withBridgeLock(configDir, gameID, func() error {
		cp, err := NewConfigPaths(configDir)
		if err != nil {
			return fmt.Errorf("failed to create config paths: %w", err)
		}
		cfgPath := cp.GetBridgeConfigPath(gameID)
		bridge, err := readBridgeJSONFile(cfgPath)
		if err != nil {
			return err
		}
		// Test barrier: a hook fired after the read (still under the lock) lets
		// a deterministic test attempt a rotation and prove it CANNOT land until
		// the stamp completes.
		if bridgeStampAfterReadHook != nil {
			bridgeStampAfterReadHook()
		}
		if bridge.Port != expectedPort || bridge.Token != expectedToken {
			return ErrBridgeEndpointRotated
		}
		bridge.Profile = diag.Profile
		bridge.ConfigRevision = diag.ConfigRevision
		bridge.StartedAt = diag.StartedAt
		return writeBridgeJSONFile(cfgPath, bridge)
	})
}

// ErrBridgeEndpointRotated reports that bridge.json no longer carries the
// launch's endpoint (a successor rotated the token), so its diagnostics must
// not be stamped.
var ErrBridgeEndpointRotated = fmt.Errorf("bridge endpoint rotated; refusing to stamp stale diagnostics")

// WriteBridgeJSONWithEndpoint writes a specific bridge endpoint atomically,
// with NO diagnostic fields — the endpoint (port/token) is the only thing the
// env-only live contract needs. Diagnostics are stamped separately at spawn
// (StampBridgeDiagnostics).
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
