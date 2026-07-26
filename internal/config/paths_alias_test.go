package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The game directory is a per-ID trust boundary for writes as well as reads:
// an in-root symlink whose target is still inside the base passes ancestor
// containment, so only the exact-component walk keeps one game ID from
// writing into another game's directory (claim, bridge, lock, history).

func TestSafeGameDirRejectsInRootAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "realgame"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "realgame"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	cp, err := NewConfigPaths(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cp.SafeGameDir("alias"); err == nil {
		t.Fatal("an in-root symlink alias must be rejected for reads and writes alike")
	}
	if err := cp.EnsureGameDir("alias"); err == nil {
		t.Fatal("EnsureGameDir must refuse an in-root symlink alias")
	}
	if _, err := cp.SafeGameDir("realgame"); err != nil {
		t.Fatalf("the real game directory must stay usable: %v", err)
	}
}

func TestSafeGameDirAllowsNotYetExistingTail(t *testing.T) {
	dir := t.TempDir()
	cp, err := NewConfigPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cp.SafeGameDir("brand/new"); err != nil {
		t.Fatalf("a not-yet-existing nested game dir must pass (creation makes real directories): %v", err)
	}
	if err := cp.EnsureGameDir("brand/new"); err != nil {
		t.Fatalf("EnsureGameDir must create the nested tail: %v", err)
	}
	// Once created for real, the exact walk must keep accepting it.
	if _, err := cp.SafeGameDir("brand/new"); err != nil {
		t.Fatalf("an exactly-created game dir must keep passing: %v", err)
	}
}
