package mcp

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/pardeike/gabs/internal/util"
)

// TestTestConstructorLeaksNoDirectory covers F6: the test constructor's config
// dir is a caller-owned t.TempDir(), so the framework removes it and NO
// gabs-test-isolated dir is ever created — the earlier constructor-owned dir
// and its Shutdown-time RemoveAll are gone.
//
// The subtest observes the ACTUAL removal the reviewer asked to see: a subtest
// owns its own t.TempDir/t.Cleanup scope, so once t.Run returns, the framework
// has already removed the constructor's directory. Statting it afterwards
// proves removal directly rather than inferring it from a path prefix.
func TestTestConstructorLeaksNoDirectory(t *testing.T) {
	var dir, caller string
	t.Run("owns-and-runs-sequence", func(st *testing.T) {
		s := NewServerForTesting(st, util.NewLogger("error"))
		dir = s.configDir
		if dir == "" {
			st.Fatal("the test constructor must set a config dir")
		}
		// It is a framework-owned t.TempDir (under the OS temp root), not a
		// hand-rolled directory Shutdown must chase.
		if !strings.HasPrefix(dir, os.TempDir()) {
			st.Fatalf("the test config dir must be a t.TempDir under %q, got %q", os.TempDir(), dir)
		}
		if _, err := os.Stat(dir); err != nil {
			st.Fatalf("the config dir must exist while the test runs: %v", err)
		}

		// The common sequence NewServerForTesting → SetConfigDir → Shutdown must
		// leave BOTH dirs for their framework owners — Shutdown removes neither
		// (it owns only the background-task join).
		caller = st.TempDir()
		s.SetConfigDir(caller)
		s.Shutdown() // explicit call; also runs again via st.Cleanup — idempotent
		if _, err := os.Stat(caller); err != nil {
			st.Fatalf("Shutdown must not remove a caller-provided config dir: %v", err)
		}
		if _, err := os.Stat(dir); err != nil {
			st.Fatalf("Shutdown must not remove the original t.TempDir either: %v", err)
		}
	})

	// The subtest returned: its framework TempDir removal has run. BOTH the
	// constructor dir and the caller dir are now gone — proving the framework,
	// not Shutdown, owns their removal.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the framework must remove the constructor's t.TempDir: %v", err)
	}
	if _, err := os.Stat(caller); !os.IsNotExist(err) {
		t.Fatalf("the framework must remove the caller's t.TempDir: %v", err)
	}
}

// TestShutdownJoinsConcurrentBackgroundAdmission covers F3: an admitted
// background task racing Shutdown must be joined, and no positive bgWG.Add may
// race bgWG.Wait (WaitGroup misuse panics under -race). The admission helper
// serializes the shutdown check and the Add under s.mu, the same lock Shutdown
// holds when it closes admission.
func TestShutdownJoinsConcurrentBackgroundAdmission(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		s := NewServerForTesting(t, util.NewLogger("error"))
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
		s := NewServerForTesting(t, util.NewLogger("error"))
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
