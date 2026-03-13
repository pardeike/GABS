package process

import (
	"syscall"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	statusStillActive              = 259
)

var modkernel32 = syscall.NewLazyDLL("kernel32.dll")
var procGetExitCodeProcess = modkernel32.NewProc("GetExitCodeProcess")

// isProcessAlive checks whether the process with the given PID is still running.
func isProcessAlive(pid int) bool {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)

	var exitCode uint32
	r, _, _ := procGetExitCodeProcess.Call(uintptr(handle), uintptr(unsafe.Pointer(&exitCode)))
	if r == 0 {
		return false
	}
	return exitCode == statusStillActive
}
