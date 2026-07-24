package process

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/launch"
)

// M2.12 conformance cells (design/03, T-DELIV): the remaining chain shapes from
// the ownership table — a scrubbed re-exec, a filtering boundary (full and
// managed-only), a reintroduced absent name, and a detaching launcher. Each
// spawns the probe through a real wrapper, then evaluates what actually arrived
// against the spawn-pinned digests with the production EvaluateContextDelivery,
// proving the verdict can claim no more than the chain delivered. The Windows
// cmd.exe variants live in conformance_windows_test.go (run on the M2.3 lane).

// runDeliveryWrapper spawns the probe through script (an sh wrapper), waits for
// the probe report, and returns it with the spawn-pinned digests. The wrapper is
// argv[0]; REAL_TARGET is the probe binary it re-execs. PROBE_OUTPUT_FILE is a
// managed spec.Env entry, so a scrubbing/filtering wrapper must carry it across
// explicitly (it is the test's reporting channel, independent of the GABS
// channels under test) — the cells that scrub bake it back in.
func runDeliveryWrapper(t *testing.T, script string, extraEnv map[string]string) (probeReport, *RuntimeContextDigests) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "wrapper.sh")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"REAL_TARGET": exe}
	for k, v := range extraEnv {
		env[k] = v
	}
	spec := resolvedProbeSpec(t, wrapper, dir, env)

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

// digestsForProbe pins the expected delivery with the production
// ComputeContextDigests over values the test controls: the managed layer read
// from the controller's materialized environment, the config-declared context
// keys as the context channel, and the absent names. The managed/context split
// mirrors production but cannot change any cell's overall verdict (a missing
// context key fails a channel either way), so no production classifier is
// duplicated.
func digestsForProbe(t *testing.T, c interface{ FinalEnvironment() []string }, spec LaunchSpec) *RuntimeContextDigests {
	t.Helper()
	finalEnv := map[string]string{}
	for _, kv := range c.FinalEnvironment() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			finalEnv[kv[:i]] = kv[i+1:]
		}
	}
	contextKeys := map[string]bool{}
	for _, k := range spec.ContextEnvKeys {
		contextKeys[k] = true
	}
	managed := map[string]string{}
	context := map[string]string{}
	for _, name := range strings.Split(finalEnv["GABS_FORWARD_ENV"], ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		v, ok := finalEnv[name]
		if !ok {
			continue
		}
		if contextKeys[name] {
			context[name] = v
		} else {
			managed[name] = v
		}
	}
	d, err := ComputeContextDigests(ArgvPayloadForDigest(spec.PathOrId, spec.Args), spec.WorkingDir, false, managed, context, spec.AbsentEnvNames)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// observedFromReport reconstructs the bridge's welcome-time report from what the
// probe actually saw: the forwarded env names carry their observed values, and
// each absent name is confirmed absent unless the probe saw it (a reintroduced
// name lands in EnvValues, which fails the channel like a wrong value).
func observedFromReport(r probeReport, d *RuntimeContextDigests) *ObservedContext {
	obs := &ObservedContext{Argv: r.Argv, Cwd: r.Cwd, EnvValues: map[string]string{}}
	record := func(name string) {
		if v, ok := r.Env[name]; ok {
			obs.EnvValues[name] = v
		}
	}
	for k := range d.ManagedEnvSHA256 {
		record(k)
	}
	for k := range d.ContextEnvSHA256 {
		record(k)
	}
	for _, n := range d.AbsentEnvNames {
		if v, ok := r.Env[n]; ok {
			obs.EnvValues[n] = v // reintroduced by the boundary → present
		} else {
			obs.EnvAbsent = append(obs.EnvAbsent, n) // positively confirmed absent
		}
	}
	return obs
}

// Filtering boundary (container simulation) that forwards exactly the names in
// GABS_FORWARD_ENV: the full declared context arrives and delivery verifies.
func TestConformanceFilteringWrapperFullContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh filtering wrapper; the cmd.exe variant runs on the Windows CI lane")
	}
	report, d := runDeliveryWrapper(t, filteringScript(false), nil)
	if report.Env["CONTENT_SET"] != "combat" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("filtering wrapper dropped a forwarded name: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryVerified {
		t.Fatalf("a full GABS_FORWARD_ENV forward must verify: %s (channels %v; reasons %v)", v.Overall, v.Channels, v.Reasons)
	}
}

// Filtering boundary that forwards only the GABS_/GABP_ managed names and drops
// the config-context keys: the context env channel cannot verify, so the verdict
// is partial — it never claims more than was delivered.
func TestConformanceFilteringWrapperManagedOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh filtering wrapper; the cmd.exe variant runs on the Windows CI lane")
	}
	report, d := runDeliveryWrapper(t, filteringScript(true), nil)
	if _, ok := report.Env["CONTENT_SET"]; ok {
		t.Fatalf("managed-only wrapper must drop the context key: %v", report.Env)
	}
	if report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("managed names must still arrive: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryOverallPartial {
		t.Fatalf("dropping the context key must yield partial: %s (channels %v; reasons %v)", v.Overall, v.Channels, v.Reasons)
	}
	if v.Channels[DeliveryChannelContextEnv] == DeliveryVerified {
		t.Fatalf("the context env channel must not verify: %v", v.Channels)
	}
}

// Scrubbed re-exec (Steam-style): the workload inherits no GABS/GABP/context
// env. argv and cwd still arrive, so the verdict is partial — not a false
// verified, and not a silent unknown that hides the delivered channels.
func TestConformanceEnvDroppingWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh scrubbing wrapper; the cmd.exe variant runs on the Windows CI lane")
	}
	script := "#!/bin/sh\n" +
		"# Scrub the whole environment except the test's reporting channel.\n" +
		"exec env -i PROBE_OUTPUT_FILE=\"$PROBE_OUTPUT_FILE\" \"$REAL_TARGET\" \"$@\"\n"
	report, d := runDeliveryWrapper(t, script, nil)
	if _, ok := report.Env["CONTENT_SET"]; ok {
		t.Fatalf("a scrubbed re-exec must deliver no context env: %v", report.Env)
	}
	if _, ok := report.Env["GABS_PROFILE"]; ok {
		t.Fatalf("a scrubbed re-exec must deliver no managed env: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryOverallPartial {
		t.Fatalf("scrubbed env with argv/cwd intact must be partial: %s (channels %v; reasons %v)", v.Overall, v.Channels, v.Reasons)
	}
	if v.Channels[DeliveryChannelArgv] != DeliveryVerified || v.Channels[DeliveryChannelCwd] != DeliveryVerified {
		t.Fatalf("argv and cwd must still verify through a scrub: %v", v.Channels)
	}
}

// A boundary that reintroduces a GABS_ABSENT_ENV name (a container image
// defining it) defeats the profile's isolation: the env channel fails exactly
// like a wrong value, and the overall verdict is partial.
func TestConformanceAbsentEnvReintroduced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh wrapper; the cmd.exe variant runs on the Windows CI lane")
	}
	script := "#!/bin/sh\n" +
		"# Reintroduce an absent name, then forward everything else unchanged.\n" +
		"exec env HOST_OVERRIDE=reintroduced \"$REAL_TARGET\" \"$@\"\n"
	report, d := runDeliveryWrapper(t, script, nil)
	if report.Env["HOST_OVERRIDE"] != "reintroduced" {
		t.Fatalf("the reintroduced name must be observed present: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryOverallPartial {
		t.Fatalf("a reintroduced absent name must fail the env channel: %s (channels %v; reasons %v)", v.Overall, v.Channels, v.Reasons)
	}
}

// Detaching launcher (double-fork): the launcher spawns the workload in the
// background and exits, so the workload is reparented — the injected context
// still arrives and verifies.
func TestConformanceDetachedWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh double-fork wrapper; the cmd.exe start variant runs on the Windows CI lane")
	}
	script := "#!/bin/sh\n" +
		"# Detach: background the workload and exit, reparenting it.\n" +
		"\"$REAL_TARGET\" \"$@\" &\n" +
		"exit 0\n"
	report, d := runDeliveryWrapper(t, script, nil)
	if report.Env["CONTENT_SET"] != "combat" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("a detached workload must still receive the context: %v", report.Env)
	}
	v := EvaluateContextDelivery(d, observedFromReport(report, d))
	if v.Overall != DeliveryVerified {
		t.Fatalf("delivery must survive detachment: %s (channels %v; reasons %v)", v.Overall, v.Channels, v.Reasons)
	}
}

// Executable .app bundle (design/03 platform rule, T-DELIV): a macOS bundle
// target resolves to its inner executable (Contents/MacOS, per
// CFBundleExecutable) via the production resolver and is exec'd directly, so
// argv+env arrive and delivery verifies — proving actual execution, not just
// target-string resolution.
func TestConformanceAppBundleExecutesInnerBinary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS .app bundle cell")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "Probe.app")
	macOS := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(macOS, "probe-inner")
	if err := os.Symlink(exe, inner); err != nil { // inner binary is the probe
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleExecutable</key><string>probe-inner</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}

	// Production bundle resolution: the .app resolves to its inner executable.
	resolved := launch.EffectiveDirectPathTarget(app)
	if resolved != inner {
		t.Fatalf("bundle resolution = %q, want inner binary %q", resolved, inner)
	}

	spec := resolvedProbeSpec(t, resolved, dir, nil)
	c := NewController()
	if err := c.Configure(spec); err != nil {
		t.Fatal(err)
	}
	c.SetBridgeInfo(43210, "test-token")
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
		t.Fatalf("argv did not arrive through the .app inner binary: %v", report.Argv)
	}
	if report.Env["CONTENT_SET"] != "combat" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("env did not arrive through the .app inner binary: %v", report.Env)
	}
	d := digestsForProbe(t, c, spec)
	if v := EvaluateContextDelivery(d, observedFromReport(report, d)); v.Overall != DeliveryVerified {
		t.Fatalf("delivery through the .app inner binary must verify: %s (%v)", v.Overall, v.Channels)
	}
}

// filteringScript builds a container-style boundary that starts from a scrubbed
// environment and re-injects only what a wrapper is contracted to carry:
// GABS_FORWARD_ENV's names (managedOnly drops the config-context keys, keeping
// just the GABS_/GABP_/platform managed names) plus the test's reporting
// channel. Config-declared names are a portable identifier grammar (design/03),
// so the unquoted NAME=value expansion is safe.
func filteringScript(managedOnly bool) string {
	filter := ""
	if managedOnly {
		filter = "  case \"$name\" in GABS_*|GABP_*|SteamAppId|SteamGameId|SystemRoot|WINDIR) ;; *) continue;; esac\n"
	}
	return "#!/bin/sh\n" +
		"fwd=\"\"\n" +
		"IFS=','\n" +
		"for name in $GABS_FORWARD_ENV; do\n" +
		filter +
		"  eval \"val=\\$$name\"\n" +
		"  fwd=\"$fwd $name=$val\"\n" +
		"done\n" +
		"unset IFS\n" +
		"exec env -i PROBE_OUTPUT_FILE=\"$PROBE_OUTPUT_FILE\" $fwd \"$REAL_TARGET\" \"$@\"\n"
}
