package process

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTransitionLockMutualExclusion(t *testing.T) {
	dir := t.TempDir()

	l1, err := AcquireTransitionLock("game", dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open-file-description: a second handle in the same
	// process contends exactly like another process would.
	if _, err := AcquireTransitionLock("game", dir, 150*time.Millisecond); err == nil {
		t.Fatalf("second acquisition must time out while held")
	}
	l1.Release()
	l2, err := AcquireTransitionLock("game", dir, time.Second)
	if err != nil {
		t.Fatalf("release must allow the next acquisition: %v", err)
	}
	l2.Release()
}

func TestTransitionLockFileIsStableAndNeverDeleted(t *testing.T) {
	dir := t.TempDir()
	l, err := AcquireTransitionLock("game", dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	path := filepath.Join(dir, "game", "transition.lock")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file must persist after release: %v", err)
	}
}

func TestTransitionLockSerializesTransitions(t *testing.T) {
	dir := t.TempDir()
	state := NewRuntimeState(m2Spec("serial"), RuntimeStateStatusStarting)
	if err := ClaimRuntimeState("serial", dir, state); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	const perWorker = 5
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				_, err := TransitionRuntimeState("serial", dir, 5*time.Second, func(s *RuntimeState) error {
					s.Phase = PhaseActive
					return nil
				})
				if err != nil {
					t.Errorf("transition failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	final, err := LoadRuntimeState("serial", dir)
	if err != nil {
		t.Fatal(err)
	}
	// every transition bumped exactly once: no lost updates
	want := uint64(1 + workers*perWorker)
	if final.Generation != want {
		t.Fatalf("lost updates: generation %d, want %d", final.Generation, want)
	}
}
