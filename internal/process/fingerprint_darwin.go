//go:build darwin

package process

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

// sysctl kern.proc.pid.<pid>; struct kinfo_proc begins with extern_proc,
// whose first member is the start-time timeval (tv_sec int64, tv_usec int32).
const (
	ctlKern     = 1
	kernProc    = 14
	kernProcPID = 1
)

// ProcessStartTime reads the process start time via sysctl kern.proc
// (epoch microseconds). Opaque; equality-compared only. A successful call
// with zero size is how the kernel reports "no such process".
func ProcessStartTime(pid int) (int64, error) {
	mib := [4]int32{ctlKern, kernProc, kernProcPID, int32(pid)}
	buf := make([]byte, 1024)
	size := uintptr(len(buf))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
	if errno != 0 {
		if errno == syscall.ESRCH || errno == syscall.ENOENT {
			return 0, ErrProcessNotFound
		}
		return 0, fmt.Errorf("sysctl kern.proc.pid.%d: %w", pid, errno)
	}
	if size == 0 {
		return 0, ErrProcessNotFound
	}
	if size < 12 {
		return 0, fmt.Errorf("sysctl kern.proc.pid.%d: short kinfo_proc (%d bytes)", pid, size)
	}
	sec := int64(binary.LittleEndian.Uint64(buf[0:8]))
	usec := int64(int32(binary.LittleEndian.Uint32(buf[8:12])))
	return sec*1_000_000 + usec, nil
}
