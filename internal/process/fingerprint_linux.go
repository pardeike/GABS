//go:build linux

package process

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProcessStartTime reads the process start time (clock ticks since boot,
// /proc/<pid>/stat field 22). Opaque; equality-compared only.
func ProcessStartTime(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrProcessNotFound
		}
		return 0, err
	}
	// comm (field 2) is an arbitrary string in parentheses; fields are only
	// parseable after its closing paren.
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[i+1:]))
	// fields[0] is overall field 3 (state); starttime is overall field 22.
	if len(fields) < 20 {
		return 0, fmt.Errorf("short /proc/%d/stat: %d fields after comm", pid, len(fields))
	}
	return strconv.ParseInt(fields[19], 10, 64)
}
