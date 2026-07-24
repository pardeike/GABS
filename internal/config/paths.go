package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ValidateGameID enforces the one-component game-ID grammar that keeps a raw
// MCP/CLI identifier from escaping the config base (design/07): a game ID is a
// single path component — never empty, "." / "..", or containing a path
// separator or NUL. With this, filepath.Join(base, id) is LEXICALLY always a
// direct child of base, so no traversal component can reach a filesystem op.
func ValidateGameID(gameID string) error {
	switch {
	case gameID == "":
		return fmt.Errorf("game ID is required")
	case gameID == "." || gameID == "..":
		return fmt.Errorf("invalid game ID %q", gameID)
	case strings.ContainsAny(gameID, `/\`):
		return fmt.Errorf("game ID %q must not contain a path separator", gameID)
	case strings.ContainsRune(gameID, 0):
		return fmt.Errorf("game ID %q must not contain a NUL byte", gameID)
	}
	return nil
}

// SafeGameDir validates gameID and returns its game directory, proving the
// directory — when it already exists — resolves through any symlink beneath the
// canonical config base (design/07): a symlinked game dir must never redirect a
// read/lock/removal outside the base. A not-yet-existing dir passes on the
// grammar alone (the create path), since EvalSymlinks cannot resolve it and the
// grammar already guarantees a direct child.
func (cp *ConfigPaths) SafeGameDir(gameID string) (string, error) {
	if err := ValidateGameID(gameID); err != nil {
		return "", err
	}
	dir := cp.GetGameDir(gameID)
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir, nil // does not exist yet: grammar alone confines it
	}
	resolvedBase, err := filepath.EvalSymlinks(cp.baseDir)
	if err != nil {
		resolvedBase = filepath.Clean(cp.baseDir)
	}
	if !pathWithinBase(resolvedBase, resolvedDir) {
		return "", fmt.Errorf("game ID %q resolves outside the config base", gameID)
	}
	return dir, nil
}

// SafeRuntimeStatePath is SafeGameDir joined with runtime.json.
func (cp *ConfigPaths) SafeRuntimeStatePath(gameID string) (string, error) {
	dir, err := cp.SafeGameDir(gameID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime.json"), nil
}

// pathWithinBase reports whether target is base or a descendant, in the
// platform's canonical form (case-folded on Windows).
func pathWithinBase(base, target string) bool {
	if runtime.GOOS == "windows" {
		base = strings.ToLower(base)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

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

// GetRuntimeStatePath returns the path to a game's shared runtime state file.
func (cp *ConfigPaths) GetRuntimeStatePath(gameID string) string {
	return filepath.Join(cp.GetGameDir(gameID), "runtime.json")
}

// GetHistoryPath returns the path to a game's track-record history file.
func (cp *ConfigPaths) GetHistoryPath(gameID string) string {
	return filepath.Join(cp.GetGameDir(gameID), "history.json")
}

// EnsureGameDir creates the game-specific directory if it doesn't exist.
// The directory holds per-launch credentials (runtime.json, bridge.json),
// so it is private (0700) and pre-existing looser modes are tightened —
// failure to tighten is an error, never silently ignored (design/07).
func (cp *ConfigPaths) EnsureGameDir(gameID string) error {
	gameDir := cp.GetGameDir(gameID)
	if err := os.MkdirAll(gameDir, 0o700); err != nil {
		return err
	}
	fi, err := os.Stat(gameDir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(gameDir, 0o700); err != nil {
			return fmt.Errorf("game dir %s has loose permissions (%v) that cannot be tightened: %w", gameDir, fi.Mode().Perm(), err)
		}
	}
	return nil
}

// EnsureBaseDir creates the base configuration directory if it doesn't exist
func (cp *ConfigPaths) EnsureBaseDir() error {
	return os.MkdirAll(cp.baseDir, 0755)
}
