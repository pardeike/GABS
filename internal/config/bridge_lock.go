package config

import (
	"fmt"
	"path/filepath"
	"time"
)

// bridgeLockTimeout bounds bridge-lock contention so a wedged holder surfaces
// as a bounded error, never an unbounded hang (mirrors the transition lock's
// bounded acquire).
const bridgeLockTimeout = 5 * time.Second

// BridgeLockTimeout is the bound on bridge-lock contention. A caller that must
// leave room for endpoint preparation inside an operation deadline reserves
// this, so the reserve tracks the real lock bound rather than a duplicated
// literal.
func BridgeLockTimeout() time.Duration { return bridgeLockTimeout }

// withBridgeLock runs fn while holding a per-game CROSS-PROCESS exclusive
// advisory lock on <gameDir>/bridge.lock. It is:
//
//   - cross-process, not a process-local mutex: endpoint rotation
//     (PrepareBridgeEndpointForStart) and the diagnostics stamp
//     (StampBridgeDiagnostics) run OUTSIDE any held transition lock — GateStart
//     acquires and releases the transition lock internally and does NOT retain
//     it across either call — so only an OS advisory lock can stop a superseded
//     GABS generation from restoring its token/diagnostics over a successor's
//     rotated endpoint (design/06: phase/generation transitions are
//     cross-process; an in-process mutex cannot fence them).
//   - dedicated, not the runtime transition lock: the stamp runs adjacent to
//     runtime transitions, and flock/LockFileEx are not re-entrant, so reusing
//     the transition lock could deadlock.
//
// Distinct opens conflict, so the same lock also serializes goroutines within
// one process. The acquire retries under a bounded deadline — a contended write
// waits, it never spuriously errors the way a single non-blocking attempt would.
func withBridgeLock(configDir, gameID string, fn func() error) error {
	cp, err := NewConfigPaths(configDir)
	if err != nil {
		return err
	}
	// The lock file must exist before it can be locked; creating the game dir
	// first also guarantees the bridge.json write target exists.
	if err := cp.EnsureGameDir(gameID); err != nil {
		return err
	}
	path := filepath.Join(cp.GetGameDir(gameID), "bridge.lock")
	f, err := openBridgeLockFile(path)
	if err != nil {
		return err
	}
	defer f.Close()

	deadline := time.Now().Add(bridgeLockTimeout)
	for {
		err := tryBridgeLock(f)
		if err == nil {
			break
		}
		if !bridgeLockIsContention(err) {
			return fmt.Errorf("bridge lock for %s: %w", gameID, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("bridge lock for %s held past %s", gameID, bridgeLockTimeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer releaseBridgeLock(f)
	return fn()
}

// AcquireBridgeLockForTesting takes the per-game bridge lock and holds it until
// the returned release is called — so a test can contend endpoint preparation
// (PrepareBridgeEndpointForStart) deterministically. It blocks until the lock is
// actually held.
func AcquireBridgeLockForTesting(configDir, gameID string) (release func(), err error) {
	done := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		e := withBridgeLock(configDir, gameID, func() error {
			held <- nil // signal the lock is held
			<-done      // hold it until released
			return nil
		})
		if e != nil {
			held <- e // withBridgeLock failed before fn ran
		}
	}()
	if e := <-held; e != nil {
		return nil, e
	}
	return func() { close(done) }, nil
}
