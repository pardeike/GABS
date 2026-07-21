package process

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pardeike/gabs/internal/config"
)

// TransitionLock is the per-game cross-process advisory lock held for
// milliseconds around state reads and writes — never during hooks or waits
// (design/06). The lock file is stable and never deleted; OS handle release
// recovers crashes without a racy delete-recreate protocol.
type TransitionLock struct {
	file *os.File
}

// AcquireTransitionLock takes the per-game transition lock, waiting up to
// timeout. Contention past the deadline returns an error (surfaced by
// callers as a bounded operation_in_progress, never a hang).
func AcquireTransitionLock(gameID, configDir string, timeout time.Duration) (*TransitionLock, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create config paths: %w", err)
	}
	gameDir := cp.GetGameDir(gameID)
	if err := os.MkdirAll(gameDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create game dir for transition lock: %w", err)
	}
	path := filepath.Join(gameDir, "transition.lock")

	deadline := time.Now().Add(timeout)
	for {
		lock, err := tryLockFile(path)
		if err == nil {
			return lock, nil
		}
		if !isLockContention(err) {
			return nil, fmt.Errorf("failed to acquire transition lock for %s: %w", gameID, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("transition lock for %s is held by another operation", gameID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Release drops the lock. The lock file itself persists.
func (l *TransitionLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	unlockFile(l.file)
	l.file.Close()
	l.file = nil
}
