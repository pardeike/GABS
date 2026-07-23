package mcp

import (
	"os"
	"sync"
	"testing"

	"github.com/pardeike/gabs/internal/util"
)

// TestShutdownRemovesOwnedTempDir covers F6: the constructor-created isolated
// directory is removed by Shutdown (after the joins), and a caller-provided
// SetConfigDir directory is never touched.
func TestShutdownRemovesOwnedTempDir(t *testing.T) {
	s := NewServerForTesting(util.NewLogger("error"))
	owned := s.ownedTempDir
	if owned == "" {
		t.Fatal("the test constructor must own an isolated temp dir")
	}
	if _, err := os.Stat(owned); err != nil {
		t.Fatalf("owned dir must exist before shutdown: %v", err)
	}

	// NewServerForTesting → SetConfigDir → Shutdown: the ORIGINAL owned dir is
	// removed; the caller dir is left for its own owner (t.TempDir).
	caller := t.TempDir()
	s.SetConfigDir(caller)
	s.Shutdown()

	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("the constructor-owned temp dir must be removed after shutdown: %v", err)
	}
	if _, err := os.Stat(caller); err != nil {
		t.Fatalf("a caller-provided config dir must not be removed by Shutdown: %v", err)
	}
}

// TestShutdownJoinsConcurrentBackgroundAdmission covers F3: an admitted
// background task racing Shutdown must be joined, and no positive bgWG.Add may
// race bgWG.Wait (WaitGroup misuse panics under -race). The admission helper
// serializes the shutdown check and the Add under s.mu, the same lock Shutdown
// holds when it closes admission.
func TestShutdownJoinsConcurrentBackgroundAdmission(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		s := NewServerForTesting(util.NewLogger("error"))
		s.SetConfigDir(t.TempDir())

		admitterDone := make(chan struct{})
		go func() {
			defer close(admitterDone)
			for i := 0; i < 8; i++ {
				if s.admitBackgroundTask() {
					// Simulate a task: it must complete before Shutdown returns.
					go func() { s.bgWG.Done() }()
				}
			}
		}()

		s.Shutdown() // races the admitter; must never Add-after-Wait
		<-admitterDone
	}
}

// TestShutdownJoinsConcurrentAttachment races real attachment publication —
// which admits the lease-refresh task — against Shutdown (F3), proving the
// production path is admission-safe.
func TestShutdownJoinsConcurrentAttachment(t *testing.T) {
	const port, token = 45999, "shutdown-race-token"
	for iter := 0; iter < 30; iter++ {
		s := NewServerForTesting(util.NewLogger("error"))
		dir := t.TempDir()
		s.SetConfigDir(dir)
		seedClaimEndpointForTest(t, dir, "adventure", port, token)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			// May be admitted (lease task started, joined by Shutdown) or
			// rejected (shutdown already began) — both are safe.
			_, _ = attachForTest(s, "adventure", port, token, func() bool { return true })
		}()

		s.Shutdown()
		wg.Wait()
	}
}
