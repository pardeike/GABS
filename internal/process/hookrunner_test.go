//go:build !windows

package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

func writeHookScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func statusHook(cmd string, args ...string) *launch.ResolvedHook {
	return &launch.ResolvedHook{
		Command: cmd, Args: args,
		TimeoutSeconds:   2,
		RunningExitCodes: []int{0}, StoppedExitCodes: []int{1},
	}
}

func TestStatusHookExitCodeContract(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		body    string
		verdict string
	}{
		{"exit 0", StatusRunning},
		{"exit 1", StatusStopped},
		{"exit 2", StatusUnknown}, // unclassified exit = unknown, never stopped
		{"exit 77", StatusUnknown},
	}
	for _, c := range cases {
		script := writeHookScript(t, dir, "hook.sh", c.body+"\n")
		verdict, res := RunStatusHook(statusHook(script), "game-1", "combat")
		if verdict != c.verdict {
			t.Fatalf("body %q: verdict %q, want %q (result %+v)", c.body, verdict, c.verdict, res)
		}
	}
}

func TestStatusHookCustomExitCodeSets(t *testing.T) {
	dir := t.TempDir()
	script := writeHookScript(t, dir, "hook.sh", "exit 3\n")
	h := statusHook(script)
	h.RunningExitCodes = []int{0, 3}
	h.StoppedExitCodes = []int{1}
	if verdict, _ := RunStatusHook(h, "g", ""); verdict != StatusRunning {
		t.Fatalf("custom running code 3 must classify running, got %q", verdict)
	}
}

func TestStatusHookExecFailureIsUnknown(t *testing.T) {
	h := statusHook("/nonexistent/hook-binary")
	verdict, res := RunStatusHook(h, "g", "")
	if verdict != StatusUnknown {
		t.Fatalf("exec failure must be unknown, got %q", verdict)
	}
	if res.ExecError == nil {
		t.Fatalf("exec error must be reported")
	}
}

func TestStatusHookTimeoutIsUnknownAndKillsTree(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-alive")
	// hook spawns a grandchild (same process group) then sleeps past the
	// timeout; the tree kill must take both down.
	script := writeHookScript(t, dir, "hook.sh",
		"(sleep 5; touch "+marker+") &\nsleep 5\n")
	h := statusHook(script)
	h.TimeoutSeconds = 1

	start := time.Now()
	verdict, res := RunStatusHook(h, "g", "")
	if verdict != StatusUnknown || !res.TimedOut {
		t.Fatalf("timeout must be unknown+TimedOut, got %q %+v", verdict, res)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout must be enforced, took %v", elapsed)
	}
	// give a killed-but-scheduled grandchild a moment, then confirm dead
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("grandchild survived the tree kill")
	}
}

func TestHookEnvironmentContract(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	script := writeHookScript(t, dir, "hook.sh", "env > "+out+"\nexit 0\n")

	t.Setenv("GABP_TOKEN", "secret-token")
	t.Setenv("GABP_SERVER_PORT", "12345")
	t.Setenv("ORDINARY_VAR", "inherited")

	h := statusHook(script)
	h.Env = map[string]string{"HOOK_VAR": "configured"}
	h.UnsetEnv = []string{"ORDINARY_VAR"}
	if verdict, res := RunStatusHook(h, "game-1", "combat"); verdict != StatusRunning {
		t.Fatalf("hook failed: %+v", res)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	if strings.Contains(env, "GABP_TOKEN") || strings.Contains(env, "GABP_SERVER_PORT") {
		t.Fatalf("hooks must never receive GABP secrets:\n%s", env)
	}
	if strings.Contains(env, "ORDINARY_VAR") {
		t.Fatalf("hook unsetEnv must remove inherited vars")
	}
	if !strings.Contains(env, "HOOK_VAR=configured") {
		t.Fatalf("hook env missing")
	}
	if !strings.Contains(env, "GABS_GAME_ID=game-1") || !strings.Contains(env, "GABS_PROFILE=combat") {
		t.Fatalf("managed identity vars missing:\n%s", env)
	}
}

func TestActionHookSuccessAndFailure(t *testing.T) {
	dir := t.TempDir()
	ok := writeHookScript(t, dir, "ok.sh", "echo stopping >&2\nexit 0\n")
	bad := writeHookScript(t, dir, "bad.sh", "echo 'no such container' >&2\nexit 3\n")

	h := &launch.ResolvedHook{Command: ok, TimeoutSeconds: 2}
	success, res := RunActionHook(h, "g", "")
	if !success || res.ExitCode != 0 {
		t.Fatalf("action success expected: %+v", res)
	}

	h = &launch.ResolvedHook{Command: bad, TimeoutSeconds: 2}
	success, res = RunActionHook(h, "g", "")
	if success {
		t.Fatalf("nonzero exit must be failure")
	}
	if !strings.Contains(res.StderrTail, "no such container") {
		t.Fatalf("stderr tail is the debugging signal, got %q", res.StderrTail)
	}
}

func TestHookOutputCapIsTail(t *testing.T) {
	dir := t.TempDir()
	// 64 KiB of filler then a distinctive final line: the cap keeps the tail
	script := writeHookScript(t, dir, "big.sh",
		"i=0; while [ $i -lt 4096 ]; do echo 'xxxxxxxxxxxxxxxx'; i=$((i+1)); done\necho FINAL-MARKER\nexit 1\n")
	h := statusHook(script)
	verdict, res := RunStatusHook(h, "g", "")
	if verdict != StatusStopped {
		t.Fatalf("exit 1 = stopped, got %q", verdict)
	}
	if len(res.StdoutTail) > hookOutputCap {
		t.Fatalf("stdout must be capped at %d, got %d", hookOutputCap, len(res.StdoutTail))
	}
	if !strings.Contains(res.StdoutTail, "FINAL-MARKER") {
		t.Fatalf("cap must keep the tail (the evidence end)")
	}
	if !res.StdoutTruncated {
		t.Fatalf("truncation must be marked")
	}
}

func TestStatusHookClippedToRemainingWindow(t *testing.T) {
	dir := t.TempDir()
	script := writeHookScript(t, dir, "slow.sh", "sleep 30\nexit 0\n")
	h := statusHook(script)
	h.TimeoutSeconds = 60 // the hook's own budget dwarfs the remaining window

	startAt := time.Now()
	verdict, res := RunStatusHookClipped(h, "g", "", 300*time.Millisecond)
	elapsed := time.Since(startAt)
	if verdict != StatusUnknown || !res.TimedOut {
		t.Fatalf("a clipped probe that ran out is unknown+timed-out, got %q %+v", verdict, res)
	}
	// The clip plus the pipe grace bounds the call — nowhere near 30s.
	if elapsed > 5*time.Second {
		t.Fatalf("clipped probe must honor the window, took %v", elapsed)
	}
}
