package process

import (
	"context"
	"sync"
)

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
	return callFindProcessesByNameContext(context.Background(), name)
}

// callFindProcessesByNameContext is the lifecycle-safe process-name lookup.
// On macOS and Windows the production implementation uses CommandContext for
// pgrep/tasklist, so an expired persisted operation cannot leave a utility
// running (or proceed to signal stale matches) after its deadline.
func callFindProcessesByNameContext(ctx context.Context, name string) ([]int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	findProcessesByNameMu.RLock()
	fn := findProcessesByNameFunc
	findProcessesByNameMu.RUnlock()
	return fn(ctx, name)
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
	if fn == nil {
		return func() {}
	}
	return SetFindProcessesByNameContextForTesting(func(ctx context.Context, name string) ([]int, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return fn(name)
	})
}

// SetFindProcessesByNameContextForTesting overrides process-name lookup with
// a context-aware probe. Deadline tests use it to model a pgrep/tasklist call
// that remains blocked until the operation cancels it.
func SetFindProcessesByNameContextForTesting(fn func(context.Context, string) ([]int, error)) func() {
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
