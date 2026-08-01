//go:build windows

package process

import (
	"os"
	"path/filepath"
	"testing"
)

// Windows counterparts of the context-delivery conformance cells (design/03,
// design/30 T-DELIV). Each spawns the probe through a real cmd.exe /c wrapper.cmd
// hop and evaluates the actual observation against the spawn-pinned digests with
// the production ArgvPayloadForDigest + EvaluateContextDelivery — so the argv
// channel VERIFIES through the documented wrapper (round-18 P1b), exactly as the
// Unix cells do, rather than being waved through observation-only. cmd.exe has no
// `env -i`, so each shape is produced with targeted `set`. These are
// written-but-unexecuted until the M2.3 windows-latest lane runs them; their
// green is the CI's to confirm (the payload-extraction proof itself runs locally
// in TestArgvPayloadForDigest, and the verdict logic is unit-tested cross-GOOS).

// runWindowsWrapperProbe spawns the probe through a cmd.exe /c wrapper.cmd hop
// and returns the probe report with the spawn-pinned digests. spec.Args is
// [/c, wrapper.cmd, <payload>]; ArgvPayloadForDigest strips the /c + wrapper
// prefix so the digest is over <payload>, which is exactly what the workload
// sees via %*.
func runWindowsWrapperProbe(t *testing.T, body string) (probeReport, *RuntimeContextDigests) {
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
	report := waitForProbeReport(t, filepath.Join(dir, "probe.json"))
	return report, digestsForProbe(t, c, spec)
}

// Forwarding wrapper (%* forwards the payload): argv+env+cwd survive the cmd hop
// and the argv channel verifies fully — the round-18 P1b requirement.
func TestConformanceForwardingWrapperWindows(t *testing.T) {
	report, d := runWindowsWrapperProbe(t, "\"%REAL_TARGET%\" %*\r\n")
	if report.Env["CONTENT_SET"] != "combat" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("env did not survive the cmd wrapper hop: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Channels[DeliveryChannelArgv] != DeliveryVerified {
		t.Fatalf("the argv channel must verify through cmd.exe /c: %v (reasons %v)", v.Channels, v.Reasons)
	}
	if v.Overall != DeliveryVerified {
		t.Fatalf("a forwarding cmd wrapper must verify: %s (channels %v)", v.Overall, v.Channels)
	}
}

// Scrubbed re-exec: unset every forwarded name (cmd.exe for-loop splits the
// comma list), then launch — no GABS/GABP/context env reaches the workload, so
// the verdict is partial (argv+cwd still verify).
func TestConformanceEnvDroppingWrapperWindows(t *testing.T) {
	report, d := runWindowsWrapperProbe(t,
		"for %%V in (%GABS_FORWARD_ENV%) do set \"%%V=\"\r\n\"%REAL_TARGET%\" %*\r\n")
	if _, ok := report.Env["CONTENT_SET"]; ok {
		t.Fatalf("a scrubbed re-exec must deliver no context env: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryOverallPartial {
		t.Fatalf("scrubbed env with argv/cwd intact must be partial: %s (channels %v)", v.Overall, v.Channels)
	}
	if v.Channels[DeliveryChannelArgv] != DeliveryVerified || v.Channels[DeliveryChannelCwd] != DeliveryVerified {
		t.Fatalf("argv and cwd must still verify through a scrub: %v", v.Channels)
	}
}

// Filtering boundary that drops only the config-context key: managed names
// survive, the context env channel does not, so the verdict is partial.
func TestConformanceFilteringWrapperManagedOnlyWindows(t *testing.T) {
	report, d := runWindowsWrapperProbe(t,
		"set \"CONTENT_SET=\"\r\n\"%REAL_TARGET%\" %*\r\n")
	if _, ok := report.Env["CONTENT_SET"]; ok {
		t.Fatalf("the context key must be dropped: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryOverallPartial {
		t.Fatalf("dropping the context key must yield partial: %s (channels %v)", v.Overall, v.Channels)
	}
	if v.Channels[DeliveryChannelContextEnv] == DeliveryVerified {
		t.Fatalf("the context env channel must not verify: %v", v.Channels)
	}
}

// A boundary that reintroduces a GABS_ABSENT_ENV name: the env channel fails
// exactly like a wrong value, and the overall verdict is partial.
func TestConformanceAbsentEnvReintroducedWindows(t *testing.T) {
	report, d := runWindowsWrapperProbe(t,
		"set \"HOST_OVERRIDE=reintroduced\"\r\n\"%REAL_TARGET%\" %*\r\n")
	if report.Env["HOST_OVERRIDE"] != "reintroduced" {
		t.Fatalf("the reintroduced name must be observed present: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryOverallPartial {
		t.Fatalf("a reintroduced absent name must fail the env channel: %s (channels %v)", v.Overall, v.Channels)
	}
}

// Detaching launcher: `start /b` runs the workload without a new window and the
// wrapper returns; the injected context still arrives and verifies.
func TestConformanceDetachedWrapperWindows(t *testing.T) {
	report, d := runWindowsWrapperProbe(t,
		"start /b \"\" \"%REAL_TARGET%\" %*\r\n")
	if report.Env["CONTENT_SET"] != "combat" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("a detached workload must still receive the context: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryVerified {
		t.Fatalf("delivery must survive detachment: %s (channels %v)", v.Overall, v.Channels)
	}
}
