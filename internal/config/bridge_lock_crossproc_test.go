package config

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestStampBlocksRotationAcrossProcesses is the round-14 F1 cross-process
// reproduction: a superseded GABS process paused inside StampBridgeDiagnostics
// (after its read, holding the bridge lock) must BLOCK a successor process's
// endpoint rotation until it releases — the exact case a process-local mutex
// cannot fence, because two GABS processes do not share it.
//
// Roles: the re-exec'd HELPER is process A — it stamps stale-A diagnostics and
// pauses under the lock. THIS process is B — it rotates the endpoint. With the
// cross-process bridge lock, B's rotation cannot proceed while A holds the lock;
// with the old sync.Map mutex, B would rotate immediately (the select below
// fails), leaving A free to restore its stale token/diagnostics.
func TestStampBlocksRotationAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	aPort, aToken, _, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false)
	if err != nil {
		t.Fatalf("prepare A: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestStampBlocksRotationHelperProcess$")
	cmd.Env = append(os.Environ(),
		"GABS_BRIDGE_LOCK_HELPER=1",
		"GABS_BRIDGE_LOCK_DIR="+dir,
		"GABS_BRIDGE_LOCK_PORT="+strconv.Itoa(aPort),
		"GABS_BRIDGE_LOCK_TOKEN="+aToken,
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	helperDone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(helperDone) }()

	// Wait until the helper (process A) is holding the lock, paused in its stamp.
	waitForFile(t, filepath.Join(dir, "holding"), 15*time.Second, out.String)

	// B's rotation from THIS process must block on the cross-process lock.
	rotated := make(chan string, 1)
	go func() {
		_, tok, _, _, _ := PrepareBridgeEndpointForStart("g", dir, nil, false)
		rotated <- tok
	}()
	select {
	case <-rotated:
		t.Fatalf("B rotated while another PROCESS held the bridge lock — the fence is not cross-process (helper: %s)", out.String())
	case <-time.After(300 * time.Millisecond):
	}

	// Release the helper; it completes its stamp and drops the lock.
	if err := os.WriteFile(filepath.Join(dir, "proceed"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}

	var bToken string
	select {
	case bToken = <-rotated:
	case <-time.After(15 * time.Second):
		t.Fatalf("B's rotation never completed after the helper released the lock (helper: %s)", out.String())
	}
	select {
	case <-helperDone:
	case <-time.After(15 * time.Second):
		t.Fatalf("helper process did not exit (helper: %s)", out.String())
	}

	// The final file must carry the SUCCESSOR's token and NOT the helper's stale
	// diagnostics: A stamped onto token A, then B rotated to token B (Prepare
	// clears diagnostics). A process-local mutex would have let A restore stale
	// token A / stale-A diagnostics over B.
	cp, _ := NewConfigPaths(dir)
	b, rerr := readBridgeJSONFile(cp.GetBridgeConfigPath("g"))
	if rerr != nil {
		t.Fatalf("read final bridge.json: %v", rerr)
	}
	if b.Token != bToken {
		t.Fatalf("cross-process fence failed: final token %q != successor %q (helper: %s)", b.Token, bToken, out.String())
	}
	if b.Profile == "stale-A" {
		t.Fatalf("stale cross-process diagnostics landed on the successor endpoint: %+v (helper: %s)", b, out.String())
	}
}

// TestStampBlocksRotationHelperProcess is process A, re-exec'd by the test
// above. Guarded by an env var so it is inert in an ordinary test run.
func TestStampBlocksRotationHelperProcess(t *testing.T) {
	if os.Getenv("GABS_BRIDGE_LOCK_HELPER") != "1" {
		return
	}
	dir := os.Getenv("GABS_BRIDGE_LOCK_DIR")
	port, _ := strconv.Atoi(os.Getenv("GABS_BRIDGE_LOCK_PORT"))
	token := os.Getenv("GABS_BRIDGE_LOCK_TOKEN")

	// Pause under the lock after the read: signal the parent, then wait for its
	// go-ahead. The bridge lock is held for the whole hook.
	bridgeStampAfterReadHook = func() {
		_ = os.WriteFile(filepath.Join(dir, "holding"), []byte("1"), 0o600)
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(dir, "proceed")); err == nil {
				return
			}
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	defer func() { bridgeStampAfterReadHook = nil }()

	_ = StampBridgeDiagnostics("g", dir, port, token, BridgeDiagnostics{Profile: "stale-A", StartedAt: "2026-01-01T00:00:00Z"})
}

// waitForFile polls until path exists or the deadline passes.
func waitForFile(t *testing.T, path string, timeout time.Duration, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s (helper: %s)", path, diag())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
