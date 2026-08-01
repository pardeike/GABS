//go:build windows

package process

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	lockKernel32     = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = lockKernel32.NewProc("LockFileEx")
	procUnlockFileEx = lockKernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errorLockViolation      = syscall.Errno(33)
)

// tryLockFile opens the stable lock file with normal sharing and takes an
// exclusive LockFileEx byte-range lock (design/06). Ordinary readers —
// antivirus, indexers, backup tools — can open the file freely; only
// another lock holder conflicts, so contention always means a real
// lifecycle operation. A crashed holder's lock releases with its handle.
func tryLockFile(path string) (*TransitionLock, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathp,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	var ov syscall.Overlapped
	r, _, callErr := procLockFileEx.Call(
		uintptr(handle),
		lockfileExclusiveLock|lockfileFailImmediately,
		0,    // reserved
		1, 0, // lock one byte at offset 0
		uintptr(unsafe.Pointer(&ov)),
	)
	if r == 0 {
		syscall.CloseHandle(handle)
		return nil, callErr
	}
	return &TransitionLock{file: os.NewFile(uintptr(handle), path)}, nil
}

// tryLockFileExisting is the no-create variant for detach-time cleanup: it
// never creates the lock file (or, transitively, the game directory), so a
// disconnect racing directory teardown cannot resurrect either.
func tryLockFileExisting(path string) (*TransitionLock, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathp,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	var ov syscall.Overlapped
	r, _, callErr := procLockFileEx.Call(
		uintptr(handle),
		lockfileExclusiveLock|lockfileFailImmediately,
		0,    // reserved
		1, 0, // lock one byte at offset 0
		uintptr(unsafe.Pointer(&ov)),
	)
	if r == 0 {
		syscall.CloseHandle(handle)
		return nil, callErr
	}
	return &TransitionLock{file: os.NewFile(uintptr(handle), path)}, nil
}

func unlockFile(f *os.File) {
	var ov syscall.Overlapped
	_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
}

func isLockContention(err error) bool {
	return errors.Is(err, errorLockViolation)
}
