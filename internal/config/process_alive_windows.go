package config

import (
	"syscall"
	"unsafe"
)

const (
	serverLockProcessQueryLimitedInformation = 0x1000
	serverLockStatusStillActive              = 259
)

var serverLockKernel32 = syscall.NewLazyDLL("kernel32.dll")
var serverLockGetExitCodeProcess = serverLockKernel32.NewProc("GetExitCodeProcess")

func serverLockProcessAlive(pid int) bool {
	handle, err := syscall.OpenProcess(serverLockProcessQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)

	var exitCode uint32
	r, _, _ := serverLockGetExitCodeProcess.Call(uintptr(handle), uintptr(unsafe.Pointer(&exitCode)))
	if r == 0 {
		return false
	}
	return exitCode == serverLockStatusStillActive
}
