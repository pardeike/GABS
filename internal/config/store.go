package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Snapshot is one immutable, published configuration state. Callers must
// treat it as read-only: every publish is a fresh parse, and launch
// resolution deep-copies whatever it needs into launch specs.
type Snapshot struct {
	Config    *GamesConfig
	Revision  string // "sha256:" + first 12 hex digits of the content hash
	ConfigDir string // absolute directory containing the config file
}

// ConfigError reports that the on-disk configuration is invalid. The last
// known good snapshot (if any) continues to serve read-only callers; new
// starts must be refused while this is non-nil.
type ConfigError struct {
	Revision string // revision of the invalid content
	Err      error  // the exact parse/validation error
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("configuration %s is invalid: %v", e.Revision, e.Err)
}

// Store owns configuration loading with automatic reload: on every
// config-dependent call it re-hashes the file and parses/publishes a new
// immutable snapshot only when the content hash changed. No filesystem
// watchers; atomic rename-based saves are caught naturally. Invalid content
// is parsed once and cached until it changes (design/09-config-reload.md).
type Store struct {
	path string

	mu            sync.Mutex
	current       *Snapshot
	currentHash   [sha256.Size]byte
	currentAbsent bool // current snapshot represents an absent file, not content
	haveGood      bool
	invalidHash   [sha256.Size]byte
	invalidErr    *ConfigError
	haveInvalid   bool
	parseCount    int // observed by tests: invalid content must parse exactly once
}

// NewStore creates a store for the given config file path.
func NewStore(configPath string) *Store {
	return &Store{path: configPath}
}

// Path returns the config file path this store watches.
func (s *Store) Path() string { return s.path }

// Snapshot returns the current configuration snapshot plus any config error.
// Exactly one of the following holds:
//   - (snap, nil): disk content is valid and published;
//   - (snap, err): disk content is invalid; snap is the last known good —
//     read-only callers proceed with it, new starts must be refused;
//   - (nil, err): no last known good exists (startup with invalid config).
func (s *Store) Snapshot() (*Snapshot, *ConfigError) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Existing empty-config behavior: absent file = default config.
			// Absence is tracked separately from any content hash: a
			// zero-byte file is invalid JSON, not the default config.
			if s.haveGood && s.currentAbsent {
				return s.current, nil
			}
			snap := s.publishLocked(nil, defaultGamesConfig())
			s.currentAbsent = true
			return snap, nil
		}
		cerr := &ConfigError{Revision: "unreadable", Err: err}
		if s.haveGood {
			return s.current, cerr
		}
		return nil, cerr
	}

	hash := sha256.Sum256(data)
	if s.haveGood && !s.currentAbsent && hash == s.currentHash {
		return s.current, nil
	}
	if s.haveInvalid && hash == s.invalidHash {
		if s.haveGood {
			return s.current, s.invalidErr
		}
		return nil, s.invalidErr
	}

	s.parseCount++
	cfg, perr := parseGamesConfig(data)
	if perr != nil {
		s.invalidHash = hash
		s.invalidErr = &ConfigError{Revision: revisionOf(hash), Err: perr}
		s.haveInvalid = true
		if s.haveGood {
			return s.current, s.invalidErr
		}
		return nil, s.invalidErr
	}

	snap := s.publishLocked(&hash, cfg)
	return snap, nil
}

func (s *Store) publishLocked(hash *[sha256.Size]byte, cfg *GamesConfig) *Snapshot {
	var h [sha256.Size]byte
	if hash != nil {
		h = *hash
	} else {
		h = sha256.Sum256(nil)
	}
	dir, err := filepath.Abs(filepath.Dir(s.path))
	if err != nil {
		dir = filepath.Dir(s.path)
	}
	snap := &Snapshot{Config: cfg, Revision: revisionOf(h), ConfigDir: dir}
	s.current = snap
	s.currentHash = h
	s.currentAbsent = false
	s.haveGood = true
	s.haveInvalid = false
	s.invalidErr = nil
	return snap
}

func revisionOf(hash [sha256.Size]byte) string {
	return fmt.Sprintf("sha256:%x", hash[:6])
}
