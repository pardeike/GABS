package process

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestConformanceProbeProcess is the probe helper from the design's
// context-delivery conformance suite (design/30-test-plan.md T-DELIV): when
// invoked as a child with PROBE_OUTPUT_FILE set, it records its argv,
// GABS_*/GABP_*/context env, and cwd, then exits.
func TestConformanceProbeProcess(t *testing.T) {
	out := os.Getenv("PROBE_OUTPUT_FILE")
	if out == "" {
		return
	}
	// stdout lands in the per-launch log file; the marker proves it.
	os.Stdout.WriteString("probe-stdout-marker\n")
	cwd, _ := os.Getwd()
	report := map[string]interface{}{
		"argv": os.Args,
		"cwd":  cwd,
		"env":  map[string]string{},
	}
	envOut := report["env"].(map[string]string)
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "GABS_") || strings.HasPrefix(upper, "GABP_") ||
			k == "CONTENT_SET" || k == "SystemRoot" || k == "WINDIR" {
			envOut[k] = kv[i+1:]
		}
	}
	data, _ := json.Marshal(report)
	_ = os.WriteFile(out, data, 0o600)
	os.Exit(0)
}

type probeReport struct {
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
}

func waitForProbeReport(t *testing.T, path string) probeReport {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			var r probeReport
			if json.Unmarshal(data, &r) == nil {
				return r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("probe report never appeared at %s", path)
	return probeReport{}
}

func resolvedProbeSpec(t *testing.T, target string, dir string, extraEnv map[string]string) LaunchSpec {
	t.Helper()
	out := filepath.Join(dir, "probe.json")
	env := map[string]string{
		"PROBE_OUTPUT_FILE": out,
		"CONTENT_SET":       "combat",
		"PATH":              os.Getenv("PATH"),
		"HOME":              os.Getenv("HOME"),
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	return LaunchSpec{
		GameId:   "probe-game",
		Mode:     "DirectPath",
		PathOrId: target,
		// "--" stops the go-test flag parser so the payload args reach the
		// probe as plain arguments.
		Args:           []string{"-test.run=TestConformanceProbeProcess", "--", "--data-root", filepath.Join(dir, "data")},
		WorkingDir:     dir,
		Profile:        "combat",
		Env:            env,
		ContextEnvKeys: []string{"CONTENT_SET"},
		AbsentEnvNames: []string{"HOST_OVERRIDE"},
		RuntimeDir:     filepath.Join(dir, "runtime"),
	}
}

// Direct launch: all three channels (argv, env, cwd) arrive at the child,
// and the managed layer carries the delivery-contract variables.
func TestConformanceDirectLaunch(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	spec := resolvedProbeSpec(t, exe, dir, nil)

	c := NewController()
	if err := c.Configure(spec); err != nil {
		t.Fatal(err)
	}
	c.SetBridgeInfo(43210, "test-token")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}

	report := waitForProbeReport(t, filepath.Join(dir, "probe.json"))

	// argv payload (excluding argv[0], which is the executable)
	wantArgs := spec.Args
	if len(report.Argv) < 1+len(wantArgs) {
		t.Fatalf("argv too short: %v", report.Argv)
	}
	for i, a := range wantArgs {
		if report.Argv[1+i] != a {
			t.Fatalf("argv[%d] = %q, want %q (argv: %v)", 1+i, report.Argv[1+i], a, report.Argv)
		}
	}
	// cwd (compare via EvalSymlinks: macOS /tmp is /private/tmp)
	wantCwd, _ := filepath.EvalSymlinks(dir)
	gotCwd, _ := filepath.EvalSymlinks(report.Cwd)
	if gotCwd != wantCwd {
		t.Fatalf("cwd = %q, want %q", report.Cwd, dir)
	}
	// env: context key + managed layer
	if report.Env["CONTENT_SET"] != "combat" {
		t.Fatalf("context env lost: %v", report.Env)
	}
	if report.Env["GABS_GAME_ID"] != "probe-game" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("managed identity vars wrong: %v", report.Env)
	}
	if report.Env["GABP_SERVER_PORT"] != "43210" || report.Env["GABP_TOKEN"] != "test-token" {
		t.Fatalf("GABP endpoint vars wrong: %v", report.Env)
	}
	if report.Env["GABS_ABSENT_ENV"] != "HOST_OVERRIDE" {
		t.Fatalf("GABS_ABSENT_ENV wrong: %v", report.Env)
	}

	// GABS_FORWARD_ENV drift assertion: the list equals the actually
	// injected managed names plus the context keys.
	forward := strings.Split(report.Env["GABS_FORWARD_ENV"], ",")
	injectedManaged := []string{}
	for k := range report.Env {
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "GABS_") || strings.HasPrefix(upper, "GABP_") {
			injectedManaged = append(injectedManaged, k)
		}
	}
	want := append(injectedManaged, "CONTENT_SET")
	// Windows platform vars are managed (and forwarded) when injected.
	for _, k := range []string{"SystemRoot", "WINDIR"} {
		if _, ok := report.Env[k]; ok {
			want = append(want, k)
		}
	}
	sort.Strings(want)
	sort.Strings(forward)
	if strings.Join(forward, ",") != strings.Join(want, ",") {
		t.Fatalf("GABS_FORWARD_ENV drift:\n got %v\nwant %v", forward, want)
	}
}

// Forwarding script wrapper: argv, env, and cwd survive the hop
// ("$@" forwarding), proving the wrapper contract for the simple case.
func TestConformanceForwardingWrapper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh wrapper cell; the cmd.exe variant runs on Windows CI")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "wrapper.sh")
	script := "#!/bin/sh\nexec \"$REAL_TARGET\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	spec := resolvedProbeSpec(t, wrapper, dir, map[string]string{"REAL_TARGET": exe})
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
		t.Fatalf("argv did not survive the wrapper hop: %v", report.Argv)
	}
	if report.Env["CONTENT_SET"] != "combat" || report.Env["GABS_PROFILE"] != "combat" {
		t.Fatalf("env did not survive the wrapper hop: %v", report.Env)
	}
}

// The child's stdout/stderr land in the per-launch log file, whose
// descriptors are not parent-owned pipes.
func TestLaunchLogCapturesChildOutput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	spec := resolvedProbeSpec(t, exe, dir, nil)

	c := &Controller{}
	if err := c.Configure(spec); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitForProbeReport(t, filepath.Join(dir, "probe.json"))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tail := c.LaunchLogTail(16 * 1024); strings.Contains(tail, "probe-stdout-marker") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child output never appeared in launch.log; tail: %q", c.LaunchLogTail(16*1024))
}

// Elevation hint mapping (unit-level; the errno only occurs on Windows).
func TestStartErrorHint(t *testing.T) {
	if hint := startErrorHintFor(syscallErrno740(), "windows"); !strings.Contains(hint, "elevation") {
		t.Fatalf("errno 740 must map to the elevation hint, got %q", hint)
	}
	if hint := startErrorHintFor(syscallErrno740(), "linux"); hint != "" {
		t.Fatalf("hint must be windows-only, got %q", hint)
	}
	if hint := startErrorHintFor(os.ErrNotExist, "windows"); hint != "" {
		t.Fatalf("unrelated errors must not hint, got %q", hint)
	}
}

// syscallErrno740 is ERROR_ELEVATION_REQUIRED; syscall.Errno is portable,
// so the mapping is testable on every platform.
func syscallErrno740() error {
	return syscall.Errno(740)
}
