package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// GameConfig represents a single game configuration
type GameConfig struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	LaunchMode      string   `json:"launchMode"`       // DirectPath|SteamAppId|EpicAppId|CustomCommand
	Target          string   `json:"target"`           // path or id
	Args            []string `json:"args,omitempty"`
	WorkingDir      string   `json:"workingDir,omitempty"`
	StopProcessName string   `json:"stopProcessName,omitempty"` // Optional process name for stopping the game
	GabpHost        string   `json:"gabpHost,omitempty"`        // Host for GABP server connection
	GabpMode        string   `json:"gabpMode,omitempty"`        // Connection mode: local|remote|connect
	Description     string   `json:"description,omitempty"`
}

// GamesConfig represents the main GABS configuration
type GamesConfig struct {
	Version string                 `json:"version"`
	Games   map[string]GameConfig `json:"games"`
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

	// If config doesn't exist, return empty config
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return &GamesConfig{
			Version: "1.0",
			Games:   make(map[string]GameConfig),
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
	game, exists := c.Games[gameID]
	return &game, exists
}

// AddGame adds or updates a game configuration
func (c *GamesConfig) AddGame(game GameConfig) {
	if c.Games == nil {
		c.Games = make(map[string]GameConfig)
	}
	c.Games[game.ID] = game
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