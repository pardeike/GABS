package process

import "sync"

// findProcessesByNameMu guards the test-injectable findProcessesByNameFunc seam.
// Production sets it once at init and never writes it, but tests restore it via
// SetFindProcessesByNameForTesting — and a background liveness poll (the startup
// GABP-connect monitor calling Controller.IsRunning) can read it in the window
// between a test's deferred restore and t.Cleanup(Shutdown) joining that
// goroutine. Guarding the seam makes every read/write race-free regardless of a
// test's cleanup ordering; the RLock is nanoseconds against the process scan.
var findProcessesByNameMu sync.RWMutex

// callFindProcessesByName invokes the current process-name lookup under a read
// lock. ALL callers must use this rather than referencing findProcessesByNameFunc
// directly, so the test seam is never read unsynchronized.
func callFindProcessesByName(name string) ([]int, error) {
	findProcessesByNameMu.RLock()
	fn := findProcessesByNameFunc
	findProcessesByNameMu.RUnlock()
	return fn(name)
}

// IsProcessAlive reports whether a PID currently exists.
func IsProcessAlive(pid int) bool {
	return isProcessAlive(pid)
}

// FindProcessesByName returns PIDs whose executable name matches name.
func FindProcessesByName(name string) ([]int, error) {
	return callFindProcessesByName(name)
}

// SetFindProcessesByNameForTesting overrides process-name lookup in tests.
func SetFindProcessesByNameForTesting(fn func(string) ([]int, error)) func() {
	findProcessesByNameMu.Lock()
	previous := findProcessesByNameFunc
	if fn != nil {
		findProcessesByNameFunc = fn
	}
	findProcessesByNameMu.Unlock()
	return func() {
		findProcessesByNameMu.Lock()
		findProcessesByNameFunc = previous
		findProcessesByNameMu.Unlock()
	}
}
