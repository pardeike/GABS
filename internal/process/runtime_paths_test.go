package process

import "testing"

// A slash-containing game ID is design-legal (arbitrary public IDs; RFC 6901
// attribution supports `/` and `~`). Its runtime claim must round-trip through
// the storage layer — claim, exists, load, ENUMERATE back to the exact ID, and
// remove — not be rejected as a "path separator" (round-19 P1 regression).
func TestSlashIDRuntimeClaimRoundTrips(t *testing.T) {
	dir := t.TempDir()
	const id = "factory/old"
	st := NewRuntimeState(LaunchSpec{GameId: id, Mode: "DirectPath", PathOrId: "/opt/x"}, RuntimeStateStatusRunning)
	st.Phase = PhaseActive
	if err := ClaimRuntimeState(id, dir, st); err != nil {
		t.Fatalf("a slash-containing game ID must be claimable: %v", err)
	}
	if !RuntimeClaimExists(id, dir) {
		t.Fatal("RuntimeClaimExists must find the slash ID's claim")
	}
	if got, err := LoadRuntimeState(id, dir); err != nil || got == nil {
		t.Fatalf("LoadRuntimeState for a slash ID: got=%v err=%v", got, err)
	}
	ids, err := ListRuntimeClaimIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range ids {
		if x == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListRuntimeClaimIDs must decode the storage key back to %q, got %v", id, ids)
	}
	if err := RemoveRuntimeState(id, dir); err != nil {
		t.Fatalf("RemoveRuntimeState for a slash ID: %v", err)
	}
	if RuntimeClaimExists(id, dir) {
		t.Fatal("the slash ID's claim must be gone after removal")
	}
}

// Traversal, absolute, and separator-escape IDs must still be refused by the
// path layer (the round-19 protections stay).
func TestUnsafeRuntimeIDsRejected(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"../victim", "/etc/passwd", `C:\Windows`, "a/../../b", ".", ".."} {
		if RuntimeClaimExists(id, dir) {
			t.Fatalf("unsafe id %q must not resolve to an existing claim", id)
		}
		if err := ClaimRuntimeState(id, dir, NewRuntimeState(LaunchSpec{GameId: id, Mode: "DirectPath", PathOrId: "/x"}, RuntimeStateStatusRunning)); err == nil {
			t.Fatalf("unsafe id %q must be refused by ClaimRuntimeState", id)
		}
	}
}
