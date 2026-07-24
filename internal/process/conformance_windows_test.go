//go:build windows

package process

import (
	"os"
	"path/filepath"
	"testing"
)

// The Windows counterpart of the forwarding-wrapper conformance cell
// (design/30-test-plan.md T-DELIV): argv, env, and cwd must survive a
// cmd.exe /c wrapper hop that forwards %*.
func TestConformanceForwardingWrapperWindows(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "wrapper.cmd")
	script := "@echo off\r\n\"%REAL_TARGET%\" %*\r\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	spec := resolvedProbeSpec(t, "cmd.exe", dir, map[string]string{"REAL_TARGET": exe})
	// cmd.exe /c wrapper.cmd <payload...>: the design's Windows script-hook
	// rule — scripts are configured explicitly via cmd.exe /c, never
	// implicitly wrapped.
	spec.Args = append([]string{"/c", wrapper}, spec.Args...)

	c := NewController()
	if err := c.Configure(spec); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}

	report := waitForProbeReport(t, filepath.Join(dir, "probe.json"))
	found := false
	for _, a := range report.Argv {
		if a == "--data-root" {
			found = true
		}
	}
	if !found {
		t.Fatalf("argv did not survive the cmd wrapper hop: %v", report.Argv)
	}
	if report.Env["CONTENT_SET"] != "combat" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("env did not survive the cmd wrapper hop: %v", report.Env)
	}
}

// runWindowsWrapperProbe spawns the probe through a cmd.exe /c wrapper.cmd hop
// (the M2.12 chain shapes, design/30 T-DELIV). cmd.exe has no `env -i`, so each
// shape is produced with targeted `set` — the observable outcome the unix cells
// assert with env -i. These are written-but-unexecuted until the M2.3
// windows-latest lane runs them; observation-only (the cmd /c payload puts /c
// and the wrapper into spec.Args, so an argv-channel digest would not line up —
// the verdict logic itself is unit-tested cross-platform in context_delivery).
func runWindowsWrapperProbe(t *testing.T, body string) probeReport {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "wrapper.cmd")
	if err := os.WriteFile(wrapper, []byte("@echo off\r\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := resolvedProbeSpec(t, "cmd.exe", dir, map[string]string{"REAL_TARGET": exe})
	spec.Args = append([]string{"/c", wrapper}, spec.Args...)

	c := NewController()
	if err := c.Configure(spec); err != nil {
		t.Fatal(err)
	}
	c.SetBridgeInfo(43210, "test-token")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	return waitForProbeReport(t, filepath.Join(dir, "probe.json"))
}

// Scrubbed re-exec: unset every forwarded name (cmd.exe for-loop splits the
// comma list), then launch — the workload sees no GABS/GABP/context env.
func TestConformanceEnvDroppingWrapperWindows(t *testing.T) {
	report := runWindowsWrapperProbe(t,
		"for %%V in (%GABS_FORWARD_ENV%) do set \"%%V=\"\r\n\"%REAL_TARGET%\" %*\r\n")
	if _, ok := report.Env["CONTENT_SET"]; ok {
		t.Fatalf("a scrubbed re-exec must deliver no context env: %v", report.Env)
	}
	if _, ok := report.Env["GABS_PROFILE"]; ok {
		t.Fatalf("a scrubbed re-exec must deliver no managed env: %v", report.Env)
	}
}

// Filtering boundary that drops only the config-context key: managed names
// survive, the context key does not.
func TestConformanceFilteringWrapperManagedOnlyWindows(t *testing.T) {
	report := runWindowsWrapperProbe(t,
		"set \"CONTENT_SET=\"\r\n\"%REAL_TARGET%\" %*\r\n")
	if _, ok := report.Env["CONTENT_SET"]; ok {
		t.Fatalf("the context key must be dropped: %v", report.Env)
	}
	if report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("managed names must still arrive: %v", report.Env)
	}
}

// A boundary that reintroduces a GABS_ABSENT_ENV name: the probe observes it
// present (the isolation-violation the env channel then flags).
func TestConformanceAbsentEnvReintroducedWindows(t *testing.T) {
	report := runWindowsWrapperProbe(t,
		"set \"HOST_OVERRIDE=reintroduced\"\r\n\"%REAL_TARGET%\" %*\r\n")
	if report.Env["HOST_OVERRIDE"] != "reintroduced" {
		t.Fatalf("the reintroduced name must be observed present: %v", report.Env)
	}
}

// Detaching launcher: `start /b` runs the workload without a new window and the
// wrapper returns; the injected context still arrives.
func TestConformanceDetachedWrapperWindows(t *testing.T) {
	report := runWindowsWrapperProbe(t,
		"start /b \"\" \"%REAL_TARGET%\" %*\r\n")
	if report.Env["CONTENT_SET"] != "combat" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("a detached workload must still receive the context: %v", report.Env)
	}
}
