package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GameConfig represents a single game configuration
type GameConfig struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	LaunchMode      string   `json:"launchMode"` // DirectPath|SteamAppId|EpicAppId|CustomCommand
	Target          string   `json:"target"`     // path or id
	Args            []string `json:"args,omitempty"`
	WorkingDir      string   `json:"workingDir,omitempty"`
	StopProcessName string   `json:"stopProcessName,omitempty"` // Optional process name for stopping the game
	Description     string   `json:"description,omitempty"`
}

// ToolNormalizationConfig configures how MCP tool names are normalized for different clients
type ToolNormalizationConfig struct {
	// EnableOpenAINormalization converts tool names to be OpenAI API compatible
	// Replaces dots with underscores and enforces 64-character limit
	EnableOpenAINormalization bool `json:"enableOpenAINormalization,omitempty"`
	// MaxToolNameLength restricts tool names to this length (default: 64 for OpenAI compatibility)
	MaxToolNameLength int `json:"maxToolNameLength,omitempty"`
	// PreserveOriginalName preserves the original MCP name in tool description or metadata
	PreserveOriginalName bool `json:"preserveOriginalName,omitempty"`
}

// GamesConfig represents the main GABS configuration
type GamesConfig struct {
	Version            string                   `json:"version"`
	Games              map[string]GameConfig    `json:"games"`
	ToolNormalization  *ToolNormalizationConfig `json:"toolNormalization,omitempty"`
}

// LoadGamesConfig loads the games configuration from the standard location
func LoadGamesConfig() (*GamesConfig, error) {
	return LoadGamesConfigFromPath("")
}

// LoadGamesConfigFromPath loads games configuration from a specific path (for testing)
func LoadGamesConfigFromPath(configPath string) (*GamesConfig, error) {
	if configPath == "" {
		var err error
		configPath, err = getGamesConfigPath()
		if err != nil {
			return nil, fmt.Errorf("failed to get config path: %w", err)
		}
	}

	// If config doesn't exist, return empty config with defaults
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return &GamesConfig{
			Version: "1.0",
			Games:   make(map[string]GameConfig),
			ToolNormalization: &ToolNormalizationConfig{
				EnableOpenAINormalization: false, // Off by default for backward compatibility
				MaxToolNameLength:         64,    // OpenAI limit
				PreserveOriginalName:      true,  // Always preserve original name
			},
		}, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config GamesConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Ensure tool normalization defaults are set if not present in config
	if config.ToolNormalization == nil {
		config.ToolNormalization = &ToolNormalizationConfig{
			EnableOpenAINormalization: false, // Off by default for backward compatibility
			MaxToolNameLength:         64,    // OpenAI limit
			PreserveOriginalName:      true,  // Always preserve original name
		}
	} else {
		// Set defaults for missing fields
		if config.ToolNormalization.MaxToolNameLength == 0 {
			config.ToolNormalization.MaxToolNameLength = 64
		}
	}

	return &config, nil
}

// SaveGamesConfig saves the games configuration to the standard location
func SaveGamesConfig(config *GamesConfig) error {
	return SaveGamesConfigToPath(config, "")
}

// SaveGamesConfigToPath saves games configuration to a specific path (for testing)
func SaveGamesConfigToPath(config *GamesConfig, configPath string) error {
	if configPath == "" {
		var err error
		configPath, err = getGamesConfigPath()
		if err != nil {
			return fmt.Errorf("failed to get config path: %w", err)
		}
	}

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
		// We need to create a copy and return a pointer to it
		// since we can't take the address of a map index directly
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
	validModes := []string{"DirectPath", "SteamAppId", "EpicAppId", "CustomCommand"}
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

	// For launcher-based games (Steam/Epic), require stopProcessName for proper game control
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
			EnableOpenAINormalization: false,
			MaxToolNameLength:         64,
			PreserveOriginalName:      true,
		}
	}
	return c.ToolNormalization
}

// getGamesConfigPath returns the path to the main GABS config file
func getGamesConfigPath() (string, error) {
	// Use ~/.gabs/ directory on all platforms as requested in the issue
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	baseDir := filepath.Join(homeDir, ".gabs")
	return filepath.Join(baseDir, "config.json"), nil
}
