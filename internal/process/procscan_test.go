package process

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The Linux process-table scan must surface inspection failures (EACCES
// under hidepid) as errors so liveness reports unknown, never a false
// stopped; process-disappearance races stay silent (design/04, design/20).
func TestLinuxScanPropagatesInspectionFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0000 does not block reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	prev := linuxProcRoot
	fake := t.TempDir()
	linuxProcRoot = fake
	t.Cleanup(func() { linuxProcRoot = prev })

	writePid := func(pid, comm string, mode os.FileMode) {
		dir := filepath.Join(fake, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("/usr/bin/"+comm+"\x00"), mode); err != nil {
			t.Fatal(err)
		}
	}

	// unreadable entries + no match: inspection failure, not stopped
	writePid("100", "other", 0o000)
	if _, err := findLinuxProcessesByName("game-bin"); err == nil {
		t.Fatalf("unreadable process entries with no match must be an error")
	}

	// a positive match wins regardless of unreadable neighbors
	writePid("200", "game-bin", 0o644)
	pids, err := findLinuxProcessesByName("game-bin")
	if err != nil || len(pids) != 1 || pids[0] != 200 {
		t.Fatalf("match must win over unreadable neighbors: %v %v", pids, err)
	}

	// a vanished process directory is an ordinary race, not a failure
	if err := os.RemoveAll(filepath.Join(fake, "100")); err != nil {
		t.Fatal(err)
	}
	writePid("300", "unrelated", 0o644)
	if err := os.RemoveAll(filepath.Join(fake, "300", "comm")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(fake, "300", "cmdline")); err != nil {
		t.Fatal(err)
	}
	pids, err = findLinuxProcessesByName("nothing-matches")
	if err != nil || len(pids) != 0 {
		t.Fatalf("clean scan with vanished entries must be empty+nil: %v %v", pids, err)
	}
}
