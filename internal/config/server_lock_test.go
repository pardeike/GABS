package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireServerLockPreventsSecondOwner(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "server.lock")

	lock, err := AcquireServerLock(lockPath)
	if err != nil {
		t.Fatalf("failed to acquire first lock: %v", err)
	}
	defer lock.Release()

	_, err = AcquireServerLock(lockPath)
	if err == nil {
		t.Fatal("expected second lock acquisition to fail")
	}

	var runningErr *ServerAlreadyRunningError
	if !errors.As(err, &runningErr) {
		t.Fatalf("expected ServerAlreadyRunningError, got %T (%v)", err, err)
	}
	if runningErr.Info.PID != os.Getpid() {
		t.Fatalf("expected lock owner pid %d, got %d", os.Getpid(), runningErr.Info.PID)
	}
}

func TestAcquireServerLockRemovesStaleOwner(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "server.lock")

	staleInfo := []byte("{\n  \"pid\": 999999,\n  \"startedAt\": \"2026-03-18T00:00:00Z\"\n}\n")
	if err := os.WriteFile(lockPath, staleInfo, 0o644); err != nil {
		t.Fatalf("failed to seed stale lock file: %v", err)
	}

	lock, err := AcquireServerLock(lockPath)
	if err != nil {
		t.Fatalf("expected stale lock to be replaced, got: %v", err)
	}
	defer lock.Release()
}

func TestAcquireServerLockRejectsFreshUnreadableLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "server.lock")

	if err := os.WriteFile(lockPath, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("failed to seed unreadable lock file: %v", err)
	}

	now := time.Now()
	if err := os.Chtimes(lockPath, now, now); err != nil {
		t.Fatalf("failed to refresh lock file time: %v", err)
	}

	_, err := AcquireServerLock(lockPath)
	if err == nil {
		t.Fatal("expected unreadable fresh lock to block startup")
	}
	if !strings.Contains(err.Error(), "already be starting") {
		t.Fatalf("expected startup guard error, got: %v", err)
	}
}
