package process

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The claim path is a trust boundary: a game ID must only ever address its own
// runtime.json through exact, non-symlink path components, and a claim leaf
// must be a regular file before it is read. These tests pin that contract.

func requireSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
}

func writeClaim(t *testing.T, configDir, storageRel string, contents string) string {
	t.Helper()
	dir := filepath.Join(configDir, filepath.FromSlash(storageRel))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runtime.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validClaim = `{"status":"stopped","phase":"idle"}`

func TestSymlinkedIntermediateComponentIsRejected(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	writeClaim(t, dir, "realgame", validClaim)
	if err := os.Symlink(filepath.Join(dir, "realgame"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}

	if RuntimeClaimExists("alias", dir) {
		t.Fatal("an in-root symlink must not make another game's claim addressable")
	}
	if state, err := LoadRuntimeState("alias", dir); err == nil && state != nil {
		t.Fatal("loading through a symlinked component must not return the aliased claim")
	}
	if !RuntimeClaimExists("realgame", dir) {
		t.Fatal("the real claim must stay addressable under its own ID")
	}
}

func TestSymlinkedClaimLeafIsNeverFollowed(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	target := writeClaim(t, dir, "victim", validClaim)
	if err := os.MkdirAll(filepath.Join(dir, "attacker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "attacker", "runtime.json")); err != nil {
		t.Fatal(err)
	}

	if RuntimeClaimExists("attacker", dir) {
		t.Fatal("a symlinked runtime.json must not count as an addressable claim")
	}
	if state, err := LoadRuntimeState("attacker", dir); err == nil {
		t.Fatalf("loading a symlinked claim leaf must error, got state=%v", state)
	}
}

func TestNonRegularClaimLeafIsNotAClaim(t *testing.T) {
	dir := t.TempDir()
	// A directory named runtime.json is a non-regular leaf on every OS.
	if err := os.MkdirAll(filepath.Join(dir, "g", "runtime.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	if RuntimeClaimExists("g", dir) {
		t.Fatal("a non-regular runtime.json must not count as an addressable claim")
	}
	ids, err := ListRuntimeClaimIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("listing must not include non-regular leaves, got %v", ids)
	}
}

func TestCorruptRegularClaimStaysAddressable(t *testing.T) {
	dir := t.TempDir()
	writeClaim(t, dir, "g", "{corrupt")

	if !RuntimeClaimExists("g", dir) {
		t.Fatal("a corrupt but regular claim must stay addressable for repair")
	}
	ids, err := ListRuntimeClaimIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "g" {
		t.Fatalf("a corrupt but regular claim must stay discoverable, got %v", ids)
	}
	if _, err := LoadRuntimeState("g", dir); err == nil {
		t.Fatal("corrupt claim content must still surface as a load error")
	}
}

func TestNestedGameIDsStillAddressTheirClaims(t *testing.T) {
	dir := t.TempDir()
	writeClaim(t, dir, "factory/old", validClaim)

	if !RuntimeClaimExists("factory/old", dir) {
		t.Fatal("a legal nested ID must address its claim")
	}
	state, err := LoadRuntimeState("factory/old", dir)
	if err != nil || state == nil {
		t.Fatalf("nested claim must load, got state=%v err=%v", state, err)
	}
	ids, err := ListRuntimeClaimIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "factory/old" {
		t.Fatalf("nested claim must list under its exact ID, got %v", ids)
	}
}

func TestInexactSpellingDoesNotAddressAClaim(t *testing.T) {
	dir := t.TempDir()
	writeClaim(t, dir, "gamea", validClaim)

	// On a case-insensitive filesystem GAMEA would stat the same file; the
	// exact-spelling walk must still refuse to address it.
	if RuntimeClaimExists("GAMEA", dir) {
		t.Fatal("a case-alias spelling must not address another ID's claim")
	}
	if state, err := LoadRuntimeState("GAMEA", dir); err != nil || state != nil {
		t.Fatalf("a case-alias spelling must read as no claim, got state=%v err=%v", state, err)
	}
}

func TestSymlinkedConfigRootIsSupported(t *testing.T) {
	requireSymlinks(t)
	real := t.TempDir()
	writeClaim(t, real, "g", validClaim)
	linkParent := t.TempDir()
	linked := filepath.Join(linkParent, "gabs-root")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}

	if !RuntimeClaimExists("g", linked) {
		t.Fatal("a symlinked config root must still address its claims")
	}
	state, err := LoadRuntimeState("g", linked)
	if err != nil || state == nil {
		t.Fatalf("claim must load through a symlinked root, got state=%v err=%v", state, err)
	}
	ids, err := ListRuntimeClaimIDs(linked)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "g" {
		t.Fatalf("claims must list through a symlinked root, got %v", ids)
	}
}

func TestRemoveUnlinksMalformedLeafWithoutTouchingTarget(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	target := writeClaim(t, dir, "victim", validClaim)
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "broken", "runtime.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := RemoveRuntimeState("broken", dir); err != nil {
		t.Fatalf("removal must be able to unlink a malformed leaf: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("the symlink leaf must be gone after removal")
	}
	if _, err := os.Lstat(target); err != nil {
		t.Fatalf("the symlink's target must be untouched: %v", err)
	}
}

func TestRemoveRefusesSymlinkedIntermediate(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	claim := writeClaim(t, dir, "realgame", validClaim)
	if err := os.Symlink(filepath.Join(dir, "realgame"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}

	if err := RemoveRuntimeState("alias", dir); err == nil {
		t.Fatal("removal through a symlinked component must be refused, not treated as absent")
	}
	if _, err := os.Lstat(claim); err != nil {
		t.Fatalf("the aliased game's claim must be untouched: %v", err)
	}
}

func TestListDoesNotTraverseSymlinkedSubdirectories(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	writeClaim(t, dir, "realgame", validClaim)
	outside := t.TempDir()
	writeClaim(t, outside, "escapee", validClaim)
	if err := os.Symlink(filepath.Join(outside, "escapee"), filepath.Join(dir, "sneaky")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "realgame"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}

	ids, err := ListRuntimeClaimIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "realgame" {
		t.Fatalf("listing must include exactly the real claim, got %v", ids)
	}
}

func TestWritesRefuseInRootAlias(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	victim := writeClaim(t, dir, "realgame", validClaim)
	if err := os.Symlink(filepath.Join(dir, "realgame"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}

	if err := SaveRuntimeState("alias", dir, RuntimeState{Status: RuntimeStateStatusRunning}); err == nil {
		t.Fatal("a claim write through an in-root alias must be refused")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != validClaim {
		t.Fatalf("the aliased game's claim must be untouched, got %q", data)
	}
}

func TestLockedRereadNeverFollowsSwappedSymlink(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	victim := writeClaim(t, dir, "victim", validClaim)
	tornPath := writeClaim(t, dir, "g", "{torn")

	// Hold the transition lock so the torn-claim re-read blocks, then swap the
	// pathname to a symlink while the reader waits — the exact window where a
	// pathname re-open would follow the symlink.
	lock, err := AcquireTransitionLock("g", dir, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		state *RuntimeState
		err   error
	}
	ch := make(chan outcome, 1)
	go func() {
		st, lerr := LoadRuntimeState("g", dir)
		ch <- outcome{st, lerr}
	}()
	time.Sleep(150 * time.Millisecond)
	if err := os.Remove(tornPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, tornPath); err != nil {
		t.Fatal(err)
	}
	lock.Release()

	got := <-ch
	if got.err == nil {
		t.Fatalf("a claim swapped to a symlink must never be read through, got state %+v", got.state)
	}
	if got.state != nil {
		t.Fatalf("no state may be returned for the swapped claim: %+v", got.state)
	}
}

func TestForgetDiscardsPermissionUnreadableClaim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are meaningless on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads through any file mode")
	}
	dir := t.TempDir()
	path := writeClaim(t, dir, "g", validClaim)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}

	data, digest, found, err := ReadRuntimeClaim("g", dir)
	if err != nil {
		t.Fatalf("an unreadable claim must stay forgettable: %v", err)
	}
	if !found || digest == "" {
		t.Fatalf("an unreadable claim must report found with an identity digest, got found=%v digest=%q", found, digest)
	}
	if data != nil {
		t.Fatal("an unreadable claim must not yield bytes")
	}

	if err := ForceForgetRuntimeClaim("g", dir, digest, false); !errors.Is(err, ErrForgetCorruptClaim) {
		t.Fatalf("forgetting an unreadable claim without discard consent must be refused, got %v", err)
	}
	if err := ForceForgetRuntimeClaim("g", dir, digest, true); err != nil {
		t.Fatalf("an explicitly confirmed discard must remove the unreadable claim: %v", err)
	}
	if RuntimeClaimExists("g", dir) {
		t.Fatal("the claim must be gone after the discard")
	}
}

func TestForgetRemovesSymlinkedLeafAsCorruptDiscard(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	target := writeClaim(t, dir, "victim", validClaim)
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "broken", "runtime.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	data, digest, found, err := ReadRuntimeClaim("broken", dir)
	if err != nil {
		t.Fatalf("a symlinked leaf must still be forgettable: %v", err)
	}
	if !found || digest == "" {
		t.Fatalf("a symlinked leaf must report found with an identity digest, got found=%v digest=%q", found, digest)
	}
	if data != nil {
		t.Fatal("a symlinked leaf must never be read through")
	}

	// The unreadable claim requires the explicit discard confirmation.
	if err := ForceForgetRuntimeClaim("broken", dir, digest, false); err == nil {
		t.Fatal("forgetting an unreadable claim without discard consent must be refused")
	}
	if err := ForceForgetRuntimeClaim("broken", dir, digest, true); err != nil {
		t.Fatalf("discarding must remove the malformed leaf: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("the symlink leaf must be gone after forget")
	}
	if _, err := os.Lstat(target); err != nil {
		t.Fatalf("the symlink's target must be untouched: %v", err)
	}
}
