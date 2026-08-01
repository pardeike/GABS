//go:build windows

package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/launch"
)

// cmdHook builds a status hook running cmd.exe /c <script> — the explicit
// interpreter spelling the validation requires on Windows.
func cmdHook(args ...string) *launch.ResolvedHook {
	return &launch.ResolvedHook{
		Command: "cmd.exe", Args: append([]string{"/c"}, args...),
		TimeoutSeconds:   5,
		RunningExitCodes: []int{0}, StoppedExitCodes: []int{1},
	}
}

func TestStatusHookExitCodeContractWindows(t *testing.T) {
	cases := []struct {
		exit    string
		verdict string
	}{
		{"0", StatusRunning},
		{"1", StatusStopped},
		{"2", StatusUnknown}, // unclassified exit = unknown, never stopped
	}
	for _, c := range cases {
		verdict, res := RunStatusHook(cmdHook("exit "+c.exit), "g", "")
		if verdict != c.verdict {
			t.Fatalf("exit %s: verdict %q, want %q (%+v)", c.exit, verdict, c.verdict, res)
		}
	}
}

func TestStatusHookTimeoutKillsJobTreeWindows(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-alive.txt")
	script := filepath.Join(dir, "hook.cmd")
	// The hook starts a detached grandchild that would write the marker
	// after ~4s, then blocks itself. TerminateJobObject must take down the
	// whole tree inside the 1s timeout.
	body := "@echo off\r\n" +
		"start /b cmd /c \"ping -n 5 127.0.0.1 >nul & echo alive > " + marker + "\"\r\n" +
		"ping -n 6 127.0.0.1 >nul\r\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	h := cmdHook(script)
	h.TimeoutSeconds = 1
	start := time.Now()
	verdict, res := RunStatusHook(h, "g", "")
	if verdict != StatusUnknown || !res.TimedOut || !res.TreeKillWarning {
		t.Fatalf("timeout must be unknown+TimedOut+TreeKillWarning: %q %+v", verdict, res)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout and direct-child reap must be bounded, took %v", elapsed)
	}
	time.Sleep(4500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("grandchild survived TerminateJobObject")
	}
}

func TestHookEnvironmentContractWindows(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")

	t.Setenv("GABP_TOKEN", "secret-token")
	t.Setenv("ORDINARY_VAR", "inherited")

	h := cmdHook("set > " + out)
	h.Env = map[string]string{"HOOK_VAR": "configured"}
	h.UnsetEnv = []string{"ordinary_var"} // folded on Windows
	if verdict, res := RunStatusHook(h, "game-1", "combat"); verdict != StatusRunning {
		t.Fatalf("hook failed: %+v", res)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.ToUpper(string(data))
	if strings.Contains(env, "GABP_TOKEN") {
		t.Fatalf("hooks must never receive GABP secrets:\n%s", env)
	}
	if strings.Contains(env, "ORDINARY_VAR") {
		t.Fatalf("unsetEnv must fold on Windows:\n%s", env)
	}
	if !strings.Contains(env, "HOOK_VAR=CONFIGURED") ||
		!strings.Contains(env, "GABS_GAME_ID=GAME-1") ||
		!strings.Contains(env, "GABS_PROFILE=COMBAT") {
		t.Fatalf("hook env incomplete:\n%s", env)
	}
}

func TestActionHookStderrTailWindows(t *testing.T) {
	h := &launch.ResolvedHook{
		Command: "cmd.exe", Args: []string{"/c", "echo no such container 1>&2 & exit 3"},
		TimeoutSeconds: 5,
	}
	success, res := RunActionHook(h, "g", "")
	if success {
		t.Fatalf("nonzero exit must be failure")
	}
	if !strings.Contains(res.StderrTail, "no such container") {
		t.Fatalf("stderr tail is the debugging signal, got %q", res.StderrTail)
	}
}

func TestTransitionLockCoexistsWithPlainReadersWindows(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireTransitionLock("g1", dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	// LockFileEx must not conflict with ordinary opens (antivirus, indexers,
	// backup tools) — only another lock holder contends.
	path := filepath.Join(filepath.Dir(runtimeStatePathForTest(t, "g1", dir)), "transition.lock")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("plain readers must not be blocked by the transition lock: %v", err)
	}
	f.Close()

	// a second lock holder still contends
	if _, err := AcquireTransitionLock("g1", dir, 100*time.Millisecond); err == nil {
		t.Fatalf("second lock acquisition must contend")
	}
}
