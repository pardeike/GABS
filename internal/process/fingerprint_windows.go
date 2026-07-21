//go:build windows

package process

import (
	"fmt"
	"syscall"
)

const errorInvalidParameter = syscall.Errno(87) // OpenProcess: no such pid

// ProcessStartTime reads the process creation time via GetProcessTimes
// (FILETIME, 100ns units since 1601). Opaque; equality-compared only.
func ProcessStartTime(pid int) (int64, error) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		if err == errorInvalidParameter {
			return 0, ErrProcessNotFound
		}
		return 0, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer syscall.CloseHandle(h)

	// A held handle keeps an exited process inspectable; only STILL_ACTIVE
	// counts as existing, and an inspection failure must surface as an
	// error (unknown), never be treated like STILL_ACTIVE.
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return 0, fmt.Errorf("GetExitCodeProcess(%d): %w", pid, err)
	}
	if code != statusStillActive {
		return 0, ErrProcessNotFound
	}

	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, fmt.Errorf("GetProcessTimes(%d): %w", pid, err)
	}
	return int64(creation.HighDateTime)<<32 | int64(creation.LowDateTime), nil
}
