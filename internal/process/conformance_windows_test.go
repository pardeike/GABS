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
