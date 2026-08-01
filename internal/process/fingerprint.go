package process

import (
	"errors"
	"fmt"
)

// ErrProcessNotFound means the lookup succeeded and the process does not
// exist — stopped-evidence. Every other inspection error is unknown-evidence
// (design/04): only a successful lookup that finds no match may say stopped.
var ErrProcessNotFound = errors.New("process not found")

// processStartTimeFunc is injectable for tests.
var processStartTimeFunc = ProcessStartTime

// VerifyPIDFingerprint checks a tracked PID against the start-time
// fingerprint recorded at launch. The value is opaque and platform-specific
// (proc ticks on Linux, epoch microseconds on macOS, FILETIME on Windows);
// it is only ever compared for equality on the same machine and boot.
// PID reuse can never match: a different start time means the recorded
// process is gone. A zero recorded value (legacy claim, pre-fingerprint)
// degrades to existence-only evidence.
func VerifyPIDFingerprint(pid int, recorded int64) (verdict string, detail string) {
	start, err := processStartTimeFunc(pid)
	if errors.Is(err, ErrProcessNotFound) {
		return StatusStopped, fmt.Sprintf("pid %d not found", pid)
	}
	if err != nil {
		return StatusUnknown, fmt.Sprintf("pid %d inspection failed: %v", pid, err)
	}
	if recorded == 0 {
		return StatusRunning, fmt.Sprintf("pid %d alive (no start-time fingerprint recorded; legacy claim)", pid)
	}
	if start != recorded {
		return StatusStopped, fmt.Sprintf("pid %d was reused (start time %d, recorded %d); the tracked process is gone", pid, start, recorded)
	}
	return StatusRunning, fmt.Sprintf("pid %d alive, start-time fingerprint matches", pid)
}
