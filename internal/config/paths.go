package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// ValidateGameID rejects identifiers that can never be a SAFE, INJECTIVE runtime
// path: empty, a NUL byte, an ABSOLUTE path (leading separator or a Windows
// drive), a backslash (a path separator on Windows, so it would alias per-OS),
// or a NON-CANONICAL form. Game IDs are otherwise arbitrary public strings
// (design/01: "existing configs remain valid, untouched"; RFC 6901 attribution
// supports `/` and `~`), so a nested `/` ID is legal and maps to a nested
// runtime directory. The canonical-form rule keeps the ID→directory mapping
// injective: "factory/../adventure", "adventure/", "a//b", and "./a" all clean
// to a directory another ID already owns, so accepting them as distinct public
// IDs would let status/history/stop for one ID read or remove another ID's
// claim. Traversal/containment beyond aliasing is still enforced structurally by
// SafeGameDir — the character set is not otherwise constrained (a character
// grammar breaks accepted configs; a canonical-FORM rule does not).
func ValidateGameID(gameID string) error {
	if gameID == "" {
		return fmt.Errorf("game ID is required")
	}
	if strings.ContainsRune(gameID, 0) {
		return fmt.Errorf("game ID %q must not contain a NUL byte", gameID)
	}
	if strings.ContainsRune(gameID, '\\') {
		return fmt.Errorf("game ID %q must not contain a backslash (a path separator on Windows)", gameID)
	}
	if isAbsoluteID(gameID) {
		return fmt.Errorf("game ID %q must not be an absolute path", gameID)
	}
	if !isCanonicalGameID(gameID) {
		return fmt.Errorf("game ID %q is not in canonical form; it aliases another ID's runtime directory (avoid '.', '..', '//' and a trailing '/')", gameID)
	}
	return nil
}

// isCanonicalGameID reports whether the ID already equals its cleaned, rooted
// slash form — the property that makes ID→runtime-directory injective. Rooting
// with "/" before path.Clean means a leading ".." cannot escape (it cleans to
// "/"), so any ".", "..", empty ("//"), or trailing-slash segment cleans to a
// different spelling and is rejected as non-canonical. path (not filepath) keeps
// this slash-based and identical on every OS (backslashes are already rejected).
func isCanonicalGameID(id string) bool {
	return strings.TrimPrefix(path.Clean("/"+id), "/") == id
}

// isAbsoluteID reports whether the ID is an absolute path on any platform — a
// leading separator, or a Windows drive prefix like "C:" — which would escape
// the config base under filepath.Join on that platform.
func isAbsoluteID(id string) bool {
	if strings.HasPrefix(id, "/") || strings.HasPrefix(id, `\`) {
		return true
	}
	if len(id) >= 2 && id[1] == ':' &&
		((id[0] >= 'A' && id[0] <= 'Z') || (id[0] >= 'a' && id[0] <= 'z')) {
		return true
	}
	return false
}

// SafeGameDir validates gameID and returns its runtime directory, proving it
// stays beneath the canonical config base (design/07) through three layers:
//   - LEXICAL: filepath.Join cleans the ID, so a `..` traversal escapes the base
//     and is rejected before any filesystem op; a plain nested `/` ID stays a
//     descendant (base/factory/old).
//   - SYMLINK: the deepest EXISTING ancestor of the directory is resolved and
//     must lie within the resolved base, so a symlinked intermediate cannot
//     redirect a read/write/lock/removal outside the base even when the leaf does
//     not exist yet (the create path is not exempt).
//   - EXACT: every already-existing component beneath the resolved base must be
//     an exact-spelling, non-symlink directory, so an in-root symlink or a
//     case/normalization alias can never redirect reads or writes into another
//     game's directory. Not-yet-existing tails pass: creation makes real
//     directories.
func (cp *ConfigPaths) SafeGameDir(gameID string) (string, error) {
	if err := ValidateGameID(gameID); err != nil {
		return "", err
	}
	dir := filepath.Join(cp.baseDir, gameID)
	if !pathStrictlyWithinBase(filepath.Clean(cp.baseDir), dir) {
		return "", fmt.Errorf("game ID %q resolves outside the config base", gameID)
	}
	// Resolve base and the dir's deepest existing ancestor the SAME way, so a
	// not-yet-created base (or a macOS /var → /private/var base) does not read as
	// an escape.
	resolvedBase, ok := deepestExistingResolved(cp.baseDir)
	if !ok {
		resolvedBase = filepath.Clean(cp.baseDir)
	}
	if resolvedAncestor, ok := deepestExistingResolved(dir); ok {
		if !pathWithinBase(resolvedBase, resolvedAncestor) {
			return "", fmt.Errorf("game ID %q resolves through a symlink outside the config base", gameID)
		}
	}
	if err := rejectAliasedExistingComponents(cp.baseDir, gameID); err != nil {
		return "", err
	}
	return dir, nil
}

// rejectAliasedExistingComponents walks gameID's already-existing path
// components beneath the resolved base and rejects any that is a symlink, a
// non-directory, or reachable only through an inexact spelling (a case or
// normalization alias on an insensitive filesystem). An in-root symlink whose
// target is still inside the base passes ancestor containment, so this exact
// walk is what keeps one game ID from addressing another game's directory.
// An alias is reported as os.ErrNotExist: the EXACT spelling has no directory,
// and readers treat that as "no claim" while writers surface the message.
func rejectAliasedExistingComponents(baseDir, gameID string) error {
	current, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing exists yet, so nothing can be aliased
		}
		return err
	}
	for _, component := range strings.Split(gameID, "/") {
		entries, err := os.ReadDir(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		exact := listingHasExactName(entries, component)
		current = filepath.Join(current, component)
		info, lerr := os.Lstat(current)
		if !exact && lerr == nil {
			// The listing may simply have raced an exact create; re-list once
			// before calling it an alias.
			if relisted, rerr := os.ReadDir(filepath.Dir(current)); rerr == nil {
				exact = listingHasExactName(relisted, component)
			}
			if !exact {
				return fmt.Errorf("game ID %q aliases an existing directory entry under the config base: %w", gameID, os.ErrNotExist)
			}
		}
		if lerr != nil {
			if os.IsNotExist(lerr) {
				return nil // the rest of the path does not exist yet
			}
			return lerr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("game directory component %s is a symlink", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("game directory component %s is not a directory", current)
		}
	}
	return nil
}

func listingHasExactName(entries []os.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

// SafeRuntimeStatePath is SafeGameDir joined with runtime.json.
func (cp *ConfigPaths) SafeRuntimeStatePath(gameID string) (string, error) {
	dir, err := cp.SafeGameDir(gameID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime.json"), nil
}

// SafeHistoryPath is SafeGameDir joined with history.json — the validated
// counterpart of GetHistoryPath, so a history READ cannot leave the config base
// through a `..` or symlinked ID (an arbitrary-file read via `doctor ../victim`).
func (cp *ConfigPaths) SafeHistoryPath(gameID string) (string, error) {
	dir, err := cp.SafeGameDir(gameID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "history.json"), nil
}

// deepestExistingResolved resolves the symlinks of the deepest existing ancestor
// of p (p itself when it exists), so a not-yet-existing leaf cannot defeat the
// symlink containment check.
func deepestExistingResolved(p string) (string, bool) {
	for {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved, true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", false
		}
		p = parent
	}
}

// pathWithinBase reports whether target is base or a descendant (case-folded on
// Windows) — used for the symlink-ancestor check, where base itself is a valid
// ancestor of a not-yet-created dir.
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

// pathStrictlyWithinBase is pathWithinBase but requires a PROPER descendant
// (never base itself) — a game directory must be under base, not base.
func pathStrictlyWithinBase(base, target string) bool {
	if runtime.GOOS == "windows" {
		base = strings.ToLower(base)
		target = strings.ToLower(target)
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	gameDir, err := cp.SafeGameDir(gameID)
	if err != nil {
		return err
	}
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
