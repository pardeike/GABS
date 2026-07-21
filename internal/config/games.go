package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GameConfig represents a single game configuration
type GameConfig struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	LaunchMode      string   `json:"launchMode"` // DirectPath|SteamAppId|SteamManaged|EpicAppId|CustomCommand
	Target          string   `json:"target"`     // path or id
	Args            []string `json:"args,omitempty"`
	WorkingDir      string   `json:"workingDir,omitempty"`
	StopProcessName string   `json:"stopProcessName,omitempty"` // Optional process name for stopping the game
	GABPMode        string   `json:"gabpMode,omitempty"`
	Description     string   `json:"description,omitempty"`

	// Launch-profile extensions (design/01-config-schema.md). All optional;
	// legacy entries without them behave bit-for-bit as before.
	Env            map[string]string            `json:"env,omitempty"`
	UnsetEnv       []string                     `json:"unsetEnv,omitempty"`
	DefaultProfile string                       `json:"defaultProfile,omitempty"`
	Profiles       map[string]ProfileConfig     `json:"profiles,omitempty"`
	LaunchInputs   map[string]LaunchInputConfig `json:"launchInputs,omitempty"`
	Lifecycle      *LifecycleConfig             `json:"lifecycle,omitempty"`
}

// ToolNormalizationConfig configures how MCP tool names are normalized for different clients
type ToolNormalizationConfig struct {
	// EnableOpenAINormalization converts public MCP tool names to the strict-safe
	// subset accepted by clients that reject dotted tool names.
	EnableOpenAINormalization bool `json:"enableOpenAINormalization,omitempty"`
	// MaxToolNameLength restricts tool names to this length (default: 64 for OpenAI compatibility)
	MaxToolNameLength int `json:"maxToolNameLength,omitempty"`
	// PreserveOriginalName preserves the original MCP name in tool description or metadata
	PreserveOriginalName bool `json:"preserveOriginalName,omitempty"`
}

// PortRange represents a min-max port range
type PortRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// PortRangeConfig configures custom port ranges for game bridge connections
type PortRangeConfig struct {
	// CustomRanges allows specifying custom port ranges for bridge connections
	// If empty, default ranges will be used
	CustomRanges []PortRange `json:"customRanges,omitempty"`
}

// StartupTimeoutsConfig configures startup-related wait windows in seconds.
type StartupTimeoutsConfig struct {
	ProcessStartSeconds int `json:"processStartSeconds,omitempty"`
	GABPConnectSeconds  int `json:"gabpConnectSeconds,omitempty"`
}

// SessionTimeoutsConfig configures cross-session coordination windows.
type SessionTimeoutsConfig struct {
	OwnerLeaseSeconds int `json:"ownerLeaseSeconds,omitempty"`
}

// TimeoutsConfig groups configurable timeout settings.
type TimeoutsConfig struct {
	Startup *StartupTimeoutsConfig `json:"startup,omitempty"`
	Session *SessionTimeoutsConfig `json:"session,omitempty"`
}

// GamesConfig represents the main GABS configuration
type GamesConfig struct {
	Version           string                   `json:"version"`
	Games             map[string]GameConfig    `json:"games"`
	ToolNormalization *ToolNormalizationConfig `json:"toolNormalization,omitempty"`
	APIKey            string                   `json:"apiKey,omitempty"`            // API key for HTTP server authentication
	PortRanges        *PortRangeConfig         `json:"portRanges,omitempty"`        // Custom port ranges for bridge connections
	Timeouts          *TimeoutsConfig          `json:"timeouts,omitempty"`          // Configurable timeout settings
	StripOutputSchema bool                     `json:"stripOutputSchema,omitempty"` // Strip outputSchema from tools/list for MCP clients that reject non-standard fields (e.g. Claude Code)

	// Warnings collects non-fatal load findings (unknown keys outside the
	// strict subtrees). Never serialized; surfaced via show/list/doctor.
	Warnings []ConfigIssue `json:"-"`
}

const (
	defaultProcessStartTimeoutSeconds = 10
	defaultGABPConnectTimeoutSeconds  = 60
	defaultOwnerLeaseSeconds          = 30
)

// LoadGamesConfig loads the games configuration from the standard location
func LoadGamesConfig() (*GamesConfig, error) {
	return LoadGamesConfigFromDir("")
}

// LoadGamesConfigFromDir loads games configuration from the specified config directory
// If configDir is empty, uses the default location
func LoadGamesConfigFromDir(configDir string) (*GamesConfig, error) {
	cp, err := NewConfigPaths(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create config paths: %w", err)
	}
	return LoadGamesConfigFromPath(cp.GetMainConfigPath())
}

// LoadGamesConfigFromPath loads games configuration from a specific path (for testing)
func LoadGamesConfigFromPath(configPath string) (*GamesConfig, error) {
	// If config doesn't exist, return empty config with defaults
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return &GamesConfig{
			Version: "1.0",
			Games:   make(map[string]GameConfig),
			ToolNormalization: &ToolNormalizationConfig{
				EnableOpenAINormalization: true, // Strict-safe by default
				MaxToolNameLength:         64,   // OpenAI limit
				PreserveOriginalName:      true, // Always preserve original name
			},
			PortRanges: &PortRangeConfig{}, // Empty - will use defaults
		}, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Duplicate object members must be rejected before any decoded form is
	// accepted: both struct and map decoding silently keep the last value.
	dupes, err := scanDuplicateMembers(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	if len(dupes) > 0 {
		return nil, &ValidationError{Issues: dupes}
	}

	var config GamesConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Unknown keys: errors inside the new strict subtrees, warnings elsewhere.
	ukErrs, ukWarns, err := checkUnknownKeys(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Extension validation (profiles, launch inputs, lifecycle gate).
	// Deliberately NOT the legacy per-game Validate(): loading never ran it,
	// and existing configs must keep loading exactly as before.
	extErrs := ukErrs
	opts := DefaultValidationOptions()
	gameIDs := make([]string, 0, len(config.Games))
	for id := range config.Games {
		gameIDs = append(gameIDs, id)
	}
	sort.Strings(gameIDs)
	for _, id := range gameIDs {
		g := config.Games[id]
		errsG, warnsG := ValidateGameExtensions(id, &g, opts)
		extErrs = append(extErrs, errsG...)
		ukWarns = append(ukWarns, warnsG...)
	}
	if len(extErrs) > 0 {
		return nil, &ValidationError{Issues: extErrs}
	}
	config.Warnings = ukWarns

	// Ensure tool normalization defaults are set if not present in config
	if config.ToolNormalization == nil {
		config.ToolNormalization = &ToolNormalizationConfig{
			EnableOpenAINormalization: true, // Strict-safe by default
			MaxToolNameLength:         64,   // OpenAI limit
			PreserveOriginalName:      true, // Always preserve original name
		}
	} else {
		// Set defaults for missing fields
		if config.ToolNormalization.MaxToolNameLength == 0 {
			config.ToolNormalization.MaxToolNameLength = 64
		}
	}

	// Initialize port ranges if not present (defaults handled in bridge config)
	if config.PortRanges == nil {
		config.PortRanges = &PortRangeConfig{}
	}

	// Initialize timeout defaults for explicitly configured timeout sections.
	if config.Timeouts != nil {
		if config.Timeouts.Startup != nil {
			if config.Timeouts.Startup.ProcessStartSeconds <= 0 {
				config.Timeouts.Startup.ProcessStartSeconds = defaultProcessStartTimeoutSeconds
			}
			if config.Timeouts.Startup.GABPConnectSeconds <= 0 {
				config.Timeouts.Startup.GABPConnectSeconds = defaultGABPConnectTimeoutSeconds
			}
		}
		if config.Timeouts.Session != nil {
			if config.Timeouts.Session.OwnerLeaseSeconds <= 0 {
				config.Timeouts.Session.OwnerLeaseSeconds = defaultOwnerLeaseSeconds
			}
		}
	} else {
		config.Timeouts = nil
	}

	return &config, nil
}

// SaveGamesConfig saves the games configuration to the standard location
func SaveGamesConfig(config *GamesConfig) error {
	return SaveGamesConfigToDir(config, "")
}

// SaveGamesConfigToDir saves games configuration to the specified config directory
// If configDir is empty, uses the default location
func SaveGamesConfigToDir(config *GamesConfig, configDir string) error {
	cp, err := NewConfigPaths(configDir)
	if err != nil {
		return fmt.Errorf("failed to create config paths: %w", err)
	}
	return SaveGamesConfigToPath(config, cp.GetMainConfigPath())
}

// SaveGamesConfigToPath saves games configuration to a specific path (for testing)
func SaveGamesConfigToPath(config *GamesConfig, configPath string) error {
	// Create directory if it doesn't exist
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal with pretty printing
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write atomically
	tempPath := configPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}

	if err := os.Rename(tempPath, configPath); err != nil {
		os.Remove(tempPath) // cleanup
		return fmt.Errorf("failed to rename temp config: %w", err)
	}

	return nil
}

// GetGame returns a game configuration by ID
func (c *GamesConfig) GetGame(gameID string) (*GameConfig, bool) {
	if game, exists := c.Games[gameID]; exists {
		// Return a pointer to the map value directly to maintain linkage
		// Note: This requires changing the map to store pointers instead of values
		// For now, returning a copy pointer as this is the safest approach
		gameCopy := game
		return &gameCopy, true
	}
	return nil, false
}

// AddGame adds or updates a game configuration after validation
func (c *GamesConfig) AddGame(game GameConfig) error {
	if err := game.Validate(); err != nil {
		return err
	}
	if c.Games == nil {
		c.Games = make(map[string]GameConfig)
	}
	c.Games[game.ID] = game
	return nil
}

// Validate checks if the game configuration is valid
func (g *GameConfig) Validate() error {
	if g.ID == "" {
		return fmt.Errorf("game ID is required")
	}
	if g.Name == "" {
		return fmt.Errorf("game name is required")
	}
	if g.LaunchMode == "" {
		return fmt.Errorf("launch mode is required")
	}
	// Allow empty Target for minimal configurations in automated environments
	// The user can set it manually later if needed
	if g.Target == "" && g.LaunchMode != "DirectPath" {
		return fmt.Errorf("target is required for %s launch mode", g.LaunchMode)
	}

	// Validate launch mode
	validModes := []string{"DirectPath", "SteamAppId", "SteamManaged", "EpicAppId", "CustomCommand"}
	isValidMode := false
	for _, mode := range validModes {
		if g.LaunchMode == mode {
			isValidMode = true
			break
		}
	}
	if !isValidMode {
		return fmt.Errorf("invalid launch mode '%s', must be one of: %s", g.LaunchMode, strings.Join(validModes, ", "))
	}

	// For launcher-based games (Steam/Epic), require stopProcessName for proper game control.
	// SteamManaged launches the resolved game executable directly, so it can be
	// tracked like DirectPath while still using the Steam app id for discovery.
	if g.LaunchMode == "SteamAppId" || g.LaunchMode == "EpicAppId" {
		if g.StopProcessName == "" {
			return fmt.Errorf("stopProcessName is required for %s games to enable proper game termination. Without it, GABS can only stop the launcher process, not the actual game", g.LaunchMode)
		}
	}

	return nil
}

// RemoveGame removes a game configuration
func (c *GamesConfig) RemoveGame(gameID string) bool {
	if _, exists := c.Games[gameID]; exists {
		delete(c.Games, gameID)
		return true
	}
	return false
}

// ListGames returns all configured games
func (c *GamesConfig) ListGames() []GameConfig {
	games := make([]GameConfig, 0, len(c.Games))
	for _, game := range c.Games {
		games = append(games, game)
	}
	return games
}

// GetToolNormalization returns tool normalization settings with defaults
func (c *GamesConfig) GetToolNormalization() *ToolNormalizationConfig {
	if c.ToolNormalization == nil {
		return &ToolNormalizationConfig{
			EnableOpenAINormalization: true,
			MaxToolNameLength:         64,
			PreserveOriginalName:      true,
		}
	}
	return c.ToolNormalization
}

func defaultStartupTimeoutsConfig() *StartupTimeoutsConfig {
	return &StartupTimeoutsConfig{
		ProcessStartSeconds: defaultProcessStartTimeoutSeconds,
		GABPConnectSeconds:  defaultGABPConnectTimeoutSeconds,
	}
}

func defaultSessionTimeoutsConfig() *SessionTimeoutsConfig {
	return &SessionTimeoutsConfig{
		OwnerLeaseSeconds: defaultOwnerLeaseSeconds,
	}
}

// GetStartupTimeouts returns startup timeout settings with defaults applied.
func (c *GamesConfig) GetStartupTimeouts() (time.Duration, time.Duration) {
	startup := defaultStartupTimeoutsConfig()
	if c != nil && c.Timeouts != nil && c.Timeouts.Startup != nil {
		if c.Timeouts.Startup.ProcessStartSeconds > 0 {
			startup.ProcessStartSeconds = c.Timeouts.Startup.ProcessStartSeconds
		}
		if c.Timeouts.Startup.GABPConnectSeconds > 0 {
			startup.GABPConnectSeconds = c.Timeouts.Startup.GABPConnectSeconds
		}
	}

	return time.Duration(startup.ProcessStartSeconds) * time.Second,
		time.Duration(startup.GABPConnectSeconds) * time.Second
}

// GetSessionOwnerLease returns the runtime-owner idle lease with defaults applied.
func (c *GamesConfig) GetSessionOwnerLease() time.Duration {
	session := defaultSessionTimeoutsConfig()
	if c != nil && c.Timeouts != nil && c.Timeouts.Session != nil {
		if c.Timeouts.Session.OwnerLeaseSeconds > 0 {
			session.OwnerLeaseSeconds = c.Timeouts.Session.OwnerLeaseSeconds
		}
	}

	return time.Duration(session.OwnerLeaseSeconds) * time.Second
}
