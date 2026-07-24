package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pardeike/gabs/internal/config"
)

// ErrTransitionLockBusy marks bounded lock-acquisition timeouts so callers
// can surface them as operation_in_progress instead of a generic failure
// (design/06: lock contention is never a hang and never an unexplained
// error).
var ErrTransitionLockBusy = errors.New("transition lock held by another operation")

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
	gameDir, err := cp.SafeGameDir(gameID)
	if err != nil {
		return nil, err
	}
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
			return nil, fmt.Errorf("transition lock for %s: %w", gameID, ErrTransitionLockBusy)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// AcquireTransitionLockIfPresent takes the per-game transition lock only
// when the game directory and lock file already exist — the detach-time
// variant: cleanup of a connection record must never recreate a directory
// that teardown just removed. A missing path reports ErrNoRuntimeClaim.
func AcquireTransitionLockIfPresent(gameID, configDir string, timeout time.Duration) (*TransitionLock, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create config paths: %w", err)
	}
	gameDir, err := cp.SafeGameDir(gameID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(gameDir, "transition.lock")

	deadline := time.Now().Add(timeout)
	for {
		lock, err := tryLockFileExisting(path)
		if err == nil {
			return lock, nil
		}
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w for %s (lock path gone)", ErrNoRuntimeClaim, gameID)
		}
		if !isLockContention(err) {
			return nil, fmt.Errorf("failed to acquire transition lock for %s: %w", gameID, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("transition lock for %s: %w", gameID, ErrTransitionLockBusy)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ClearAttachmentIfCurrent removes the claim's attachment record only while
// it still carries the given connection identity within the given claim
// lifetime — the detach completion (design/06). It uses the no-create lock:
// a detach racing directory teardown silently finds nothing to do.
func ClearAttachmentIfCurrent(gameID, configDir, launchID, connectionID string, timeout time.Duration) error {
	lock, err := AcquireTransitionLockIfPresent(gameID, configDir, timeout)
	if err != nil {
		return err
	}
	defer lock.Release()

	cur, err := LoadRuntimeState(gameID, configDir)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrNoRuntimeClaim
	}
	if cur.LaunchID != launchID {
		return ErrFencingViolation
	}
	if cur.Attachment == nil || cur.Attachment.ConnectionID != connectionID {
		return ErrFencingViolation
	}
	cur.Attachment = nil
	cur.Generation++
	return SaveRuntimeState(gameID, configDir, *cur)
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
