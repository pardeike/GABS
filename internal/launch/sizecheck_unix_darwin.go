//go:build darwin

package launch

import (
	"strconv"
	"syscall"
)

func currentUnixExecLimits() (unixExecLimits, bool) {
	argMax, err := syscall.SysctlUint32("kern.argmax")
	if err != nil || argMax == 0 {
		return unixExecLimits{}, false
	}
	return unixExecLimits{
		combinedBytes:      uint64(argMax),
		pointerBytes:       uint64(strconv.IntSize / 8),
		pointerTerminators: 2,
		alignStringBytes:   true,
	}, true
}
