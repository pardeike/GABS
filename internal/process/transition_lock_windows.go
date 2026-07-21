//go:build windows

package process

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile opens the stable lock file with an exclusive sharing mode
// (share-none): a second open fails with ERROR_SHARING_VIOLATION until the
// handle closes — LockFileEx-class exclusivity without an extra dependency,
// and a crashed holder's handle releases automatically.
func tryLockFile(path string) (*TransitionLock, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathp,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // share mode: exclusive
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return &TransitionLock{file: os.NewFile(uintptr(handle), path)}, nil
}

func unlockFile(f *os.File) {
	// closing the handle releases the exclusive share
}

func isLockContention(err error) bool {
	const errorSharingViolation = syscall.Errno(32)
	return errors.Is(err, errorSharingViolation)
}
