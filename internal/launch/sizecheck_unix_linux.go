//go:build linux

package launch

import (
	"os"
	"strconv"
	"syscall"
)

const linuxDefaultStackLimit = 8 << 20

func currentUnixExecLimits() (unixExecLimits, bool) {
	var stack syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_STACK, &stack); err != nil {
		return unixExecLimits{}, false
	}
	pageBytes := uint64(os.Getpagesize())
	floor := 32 * pageBytes
	// fs/exec.c: min(RLIMIT_STACK/4, 3/4 * _STK_LIM), with a 32-page
	// historical floor. _STK_LIM is 8 MiB on the supported MMU targets.
	combined := stack.Cur / 4
	cap := uint64(linuxDefaultStackLimit / 4 * 3)
	if combined > cap {
		combined = cap
	}
	if combined < floor {
		combined = floor
	}
	return unixExecLimits{
		combinedBytes:         combined,
		perStringBytes:        floor,
		pointerBytes:          uint64(strconv.IntSize / 8),
		includeExecutablePath: true,
	}, true
}
