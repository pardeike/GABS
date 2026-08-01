//go:build windows

package config

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	bridgeLockKernel32     = syscall.NewLazyDLL("kernel32.dll")
	bridgeProcLockFileEx   = bridgeLockKernel32.NewProc("LockFileEx")
	bridgeProcUnlockFileEx = bridgeLockKernel32.NewProc("UnlockFileEx")
)

const (
	bridgeLockfileFailImmediately = 0x1
	bridgeLockfileExclusiveLock   = 0x2
	bridgeErrorLockViolation      = syscall.Errno(33)
)

// openBridgeLockFile opens (creating if absent) the per-game bridge.lock with
// normal sharing so ordinary readers never conflict — only another lock holder
// does, so contention always means a real endpoint/stamp operation. A crashed
// holder's lock releases with its handle.
func openBridgeLockFile(path string) (*os.File, error) {
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
	return os.NewFile(uintptr(handle), path), nil
}

// tryBridgeLock takes a non-blocking exclusive LockFileEx byte-range lock. A
// distinct handle — another process or goroutine — conflicts, so this
// serializes both.
func tryBridgeLock(f *os.File) error {
	var ov syscall.Overlapped
	r, _, callErr := bridgeProcLockFileEx.Call(
		f.Fd(),
		bridgeLockfileExclusiveLock|bridgeLockfileFailImmediately,
		0,    // reserved
		1, 0, // lock one byte at offset 0
		uintptr(unsafe.Pointer(&ov)),
	)
	if r == 0 {
		return callErr
	}
	return nil
}

func bridgeLockIsContention(err error) bool {
	return errors.Is(err, bridgeErrorLockViolation)
}

func releaseBridgeLock(f *os.File) {
	var ov syscall.Overlapped
	_, _, _ = bridgeProcUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
}
