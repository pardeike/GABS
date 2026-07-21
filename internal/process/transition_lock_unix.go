//go:build !windows

package process

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile opens the stable lock file and takes a non-blocking exclusive
// flock. flock locks belong to the open file description, so a crashed
// holder's lock releases with its file handle.
func tryLockFile(path string) (*TransitionLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return &TransitionLock{file: f}, nil
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func isLockContention(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
