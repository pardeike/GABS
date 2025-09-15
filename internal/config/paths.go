package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigPaths provides centralized configuration directory and path resolution
type ConfigPaths struct {
	baseDir string // Base configuration directory (either custom or default ~/.gabs)
}

// NewConfigPaths creates a ConfigPaths instance with the given base directory.
// If baseDir is empty, uses the default ~/.gabs directory.
func NewConfigPaths(baseDir string) (*ConfigPaths, error) {
	var resolvedBaseDir string
	if baseDir != "" {
		resolvedBaseDir = baseDir
	} else {
		// Use ~/.gabs/ directory on all platforms as default
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		resolvedBaseDir = filepath.Join(homeDir, ".gabs")
	}
	
	return &ConfigPaths{baseDir: resolvedBaseDir}, nil
}

// GetBaseDir returns the base configuration directory
func (cp *ConfigPaths) GetBaseDir() string {
	return cp.baseDir
}

// GetMainConfigPath returns the path to the main GABS configuration file (config.json)
func (cp *ConfigPaths) GetMainConfigPath() string {
	return filepath.Join(cp.baseDir, "config.json")
}

// GetGameDir returns the directory path for a specific game's configuration files
func (cp *ConfigPaths) GetGameDir(gameID string) string {
	return filepath.Join(cp.baseDir, gameID)
}

// GetBridgeConfigPath returns the path to a game's bridge configuration file
func (cp *ConfigPaths) GetBridgeConfigPath(gameID string) string {
	return filepath.Join(cp.GetGameDir(gameID), "bridge.json")
}

// EnsureGameDir creates the game-specific directory if it doesn't exist
func (cp *ConfigPaths) EnsureGameDir(gameID string) error {
	gameDir := cp.GetGameDir(gameID)
	return os.MkdirAll(gameDir, 0755)
}

// EnsureBaseDir creates the base configuration directory if it doesn't exist
func (cp *ConfigPaths) EnsureBaseDir() error {
	return os.MkdirAll(cp.baseDir, 0755)
}