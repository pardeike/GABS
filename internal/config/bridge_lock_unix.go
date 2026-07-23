//go:build !windows

package config

import (
	"errors"
	"os"
	"syscall"
)

// openBridgeLockFile opens (creating if absent) the stable per-game bridge.lock.
// flock locks belong to the open file description, so a crashed holder's lock
// releases with its handle — no racy delete-recreate protocol.
func openBridgeLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

// tryBridgeLock takes a non-blocking exclusive flock. A second acquisition from
// a distinct open file description — another goroutine or another process —
// conflicts, so this serializes both.
func tryBridgeLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func bridgeLockIsContention(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func releaseBridgeLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
