package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const unreadableLockGracePeriod = 5 * time.Second

// ServerLockInfo describes the server process that owns the global lock.
type ServerLockInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
}

// ServerAlreadyRunningError reports that another live GABS server owns the lock.
type ServerAlreadyRunningError struct {
	Path string
	Info ServerLockInfo
}

func (e *ServerAlreadyRunningError) Error() string {
	if !e.Info.StartedAt.IsZero() {
		return fmt.Sprintf("another GABS server is already running (pid %d, started %s)", e.Info.PID, e.Info.StartedAt.Format(time.RFC3339))
	}
	if e.Info.PID > 0 {
		return fmt.Sprintf("another GABS server is already running (pid %d)", e.Info.PID)
	}
	return "another GABS server is already running"
}

// ServerLock owns the global singleton lock for `gabs server`.
type ServerLock struct {
	path string
	file *os.File
}

// AcquireServerLock claims the global server lock, removing stale lock files when their owner is gone.
func AcquireServerLock(lockPath string) (*ServerLock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create server lock directory: %w", err)
	}

	for attempt := 0; attempt < 3; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			lock := &ServerLock{path: lockPath, file: file}
			if err := lock.writeInfo(ServerLockInfo{
				PID:       os.Getpid(),
				StartedAt: time.Now().UTC(),
			}); err != nil {
				_ = lock.Release()
				return nil, fmt.Errorf("failed to initialize server lock: %w", err)
			}
			return lock, nil
		}

		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("failed to create server lock %s: %w", lockPath, err)
		}

		existingInfo, stale, inspectErr := inspectExistingServerLock(lockPath)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if !stale {
			return nil, &ServerAlreadyRunningError{
				Path: lockPath,
				Info: existingInfo,
			}
		}

		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to remove stale server lock %s: %w", lockPath, err)
		}
	}

	return nil, fmt.Errorf("failed to acquire server lock %s after removing a stale lock", lockPath)
}

// Release frees the singleton lock.
func (l *ServerLock) Release() error {
	if l == nil {
		return nil
	}

	var errs []error
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			errs = append(errs, err)
		}
		l.file = nil
	}
	if l.path != "" {
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (l *ServerLock) writeInfo(info ServerLockInfo) error {
	if l == nil || l.file == nil {
		return fmt.Errorf("server lock file is not open")
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal server lock metadata: %w", err)
	}

	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("failed to truncate server lock: %w", err)
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to rewind server lock: %w", err)
	}
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("failed to write server lock metadata: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync server lock metadata: %w", err)
	}

	return nil
}

func inspectExistingServerLock(lockPath string) (ServerLockInfo, bool, error) {
	info, err := readServerLockInfo(lockPath)
	if err == nil {
		if info.PID > 0 && serverLockProcessAlive(info.PID) {
			return info, false, nil
		}
		return info, true, nil
	}

	stat, statErr := os.Stat(lockPath)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return ServerLockInfo{}, true, nil
		}
		return ServerLockInfo{}, false, fmt.Errorf("failed to inspect existing server lock %s: %w", lockPath, statErr)
	}

	if time.Since(stat.ModTime()) < unreadableLockGracePeriod {
		return ServerLockInfo{}, false, fmt.Errorf("another GABS server may already be starting (lock file %s is unreadable: %w)", lockPath, err)
	}

	return ServerLockInfo{}, true, nil
}

func readServerLockInfo(lockPath string) (ServerLockInfo, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return ServerLockInfo{}, fmt.Errorf("failed to read server lock: %w", err)
	}

	var info ServerLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return ServerLockInfo{}, fmt.Errorf("failed to parse server lock: %w", err)
	}

	return info, nil
}
