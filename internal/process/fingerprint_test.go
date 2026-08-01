package process

import (
	"errors"
	"os"
	"testing"
)

func TestProcessStartTimeOwnProcess(t *testing.T) {
	a, err := ProcessStartTime(os.Getpid())
	if err != nil {
		t.Fatalf("own process must be inspectable: %v", err)
	}
	if a == 0 {
		t.Fatalf("start time must be non-zero (zero means 'not recorded')")
	}
	b, err := ProcessStartTime(os.Getpid())
	if err != nil || b != a {
		t.Fatalf("start time must be stable across reads: %d vs %d (%v)", a, b, err)
	}
}

func TestProcessStartTimeNotFound(t *testing.T) {
	_, err := ProcessStartTime(99999999)
	if !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("nonexistent pid must be ErrProcessNotFound, got %v", err)
	}
}

func TestVerifyPIDFingerprint(t *testing.T) {
	pid := os.Getpid()
	start, err := ProcessStartTime(pid)
	if err != nil {
		t.Fatal(err)
	}

	if v, _ := VerifyPIDFingerprint(pid, start); v != StatusRunning {
		t.Fatalf("matching fingerprint = running, got %q", v)
	}
	if v, d := VerifyPIDFingerprint(pid, start+1); v != StatusStopped {
		t.Fatalf("mismatched fingerprint = stopped (PID reuse), got %q (%s)", v, d)
	}
	if v, _ := VerifyPIDFingerprint(99999999, 123); v != StatusStopped {
		t.Fatalf("no such process = stopped, got %q", v)
	}
	// legacy claims have no recorded fingerprint; existence is the only signal
	if v, _ := VerifyPIDFingerprint(pid, 0); v != StatusRunning {
		t.Fatalf("legacy zero fingerprint + alive = running, got %q", v)
	}

	prev := processStartTimeFunc
	processStartTimeFunc = func(int) (int64, error) { return 0, errors.New("EPERM") }
	defer func() { processStartTimeFunc = prev }()
	if v, _ := VerifyPIDFingerprint(pid, start); v != StatusUnknown {
		t.Fatalf("inspection failure = unknown, never stopped, got %q", v)
	}
}
