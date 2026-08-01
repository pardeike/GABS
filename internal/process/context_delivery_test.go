package process

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestArgvPayloadForDigest is the cross-GOOS proof of the round-18 P1b fix: the
// argv payload the workload receives excludes the documented cmd.exe /c launch
// prefix, so a cmd.exe /c wrapper.cmd hop can verify the argv channel (design/03,
// T-DELIV). This is the local proof; only the actual %* forwarding is CI-gated.
func TestArgvPayloadForDigest(t *testing.T) {
	cases := []struct {
		name     string
		pathOrId string
		args     []string
		want     []string
	}{
		{"cmd /c strips prefix", "cmd.exe", []string{"/c", "w.cmd", "--data-root", "x"}, []string{"--data-root", "x"}},
		{"cmd /C case-insensitive", "cmd.exe", []string{"/C", "w.cmd", "-p", "combat"}, []string{"-p", "combat"}},
		{"absolute cmd path", `C:\Windows\System32\cmd.exe`, []string{"/c", "w.cmd", "a", "b"}, []string{"a", "b"}},
		{"cmd basename without .exe", "cmd", []string{"/c", "w.cmd", "x"}, []string{"x"}},
		{"cmd /c wrapper with no payload", "cmd.exe", []string{"/c", "w.cmd"}, []string{}},
		{"direct path is unchanged", "/opt/game", []string{"-p", "combat"}, []string{"-p", "combat"}},
		{"unix wrapper target is unchanged", "/opt/wrapper.sh", []string{"--data-root", "x"}, []string{"--data-root", "x"}},
		{"cmd with a non-/c flag is unchanged", "cmd.exe", []string{"/k", "w.cmd", "x"}, []string{"/k", "w.cmd", "x"}},
		{"cmd with fewer than two args is unchanged", "cmd.exe", []string{"/c"}, []string{"/c"}},
		{"a game literally named cmd.exe but no /c is unchanged", "cmd.exe", []string{"-p", "combat"}, []string{"-p", "combat"}},
	}
	for _, tc := range cases {
		got := ArgvPayloadForDigest(tc.pathOrId, tc.args)
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("%s: ArgvPayloadForDigest(%q, %v) = %v, want %v", tc.name, tc.pathOrId, tc.args, got, tc.want)
		}
	}
}

func testDigestsWithCwd(t *testing.T, cwd string) *RuntimeContextDigests {
	t.Helper()
	d, err := ComputeContextDigests(
		[]string{"-profile", "combat", "-scenario", "arena"},
		cwd, false,
		map[string]string{
			"GABP_SERVER_PORT": "43210",
			"GABP_TOKEN":       "secret-token",
			"GABS_GAME_ID":     "adventure",
		},
		map[string]string{"CONTENT_SET": "combat-pack"},
		[]string{"DEBUG_OVERLAY"},
	)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// testDelivery returns pinned digests plus a fully matching observation
// over a real (canonicalizable) working directory.
func testDelivery(t *testing.T) (*RuntimeContextDigests, *ObservedContext) {
	t.Helper()
	cwd := t.TempDir()
	return testDigestsWithCwd(t, cwd), &ObservedContext{
		Argv: []string{"/opt/game/bin/game", "-profile", "combat", "-scenario", "arena"},
		Cwd:  cwd,
		EnvValues: map[string]string{
			"GABP_SERVER_PORT": "43210",
			"GABP_TOKEN":       "secret-token",
			"GABS_GAME_ID":     "adventure",
			"CONTENT_SET":      "combat-pack",
		},
		EnvAbsent: []string{"DEBUG_OVERLAY"},
	}
}

func TestContextDigestsAreSaltedAndValueFree(t *testing.T) {
	cwd := t.TempDir()
	d1 := testDigestsWithCwd(t, cwd)
	d2 := testDigestsWithCwd(t, cwd)
	if d1.Salt == d2.Salt {
		t.Fatal("each launch mints its own salt")
	}
	if d1.ArgvSHA256 == d2.ArgvSHA256 || d1.ManagedEnvSHA256["GABP_TOKEN"] == d2.ManagedEnvSHA256["GABP_TOKEN"] {
		t.Fatal("digests must be salted: identical values must not produce identical digests across launches")
	}
	for k, v := range d1.ManagedEnvSHA256 {
		if strings.Contains(v, "secret-token") {
			t.Fatalf("raw values must never appear in digests: %s=%s", k, v)
		}
	}
	for k, v := range d1.ContextEnvSHA256 {
		if strings.Contains(v, "combat-pack") {
			t.Fatalf("raw values must never appear in digests: %s=%s", k, v)
		}
	}
	if len(d1.AbsentEnvNames) != 1 || d1.AbsentEnvNames[0] != "DEBUG_OVERLAY" {
		t.Fatalf("absent NAMES (never values) are pinned: %+v", d1.AbsentEnvNames)
	}
}

func TestDeliveryFullyVerified(t *testing.T) {
	d, obs := testDelivery(t)
	del := EvaluateContextDelivery(d, obs)
	if del.Overall != DeliveryVerified {
		t.Fatalf("complete matching report must verify: %+v", del)
	}
	for _, ch := range []string{DeliveryChannelArgv, DeliveryChannelCwd, DeliveryChannelManagedEnv, DeliveryChannelContextEnv} {
		if del.Channels[ch] != DeliveryVerified {
			t.Fatalf("channel %s must be verified: %+v", ch, del)
		}
	}
}

func TestDeliveryArgvZeroExcluded(t *testing.T) {
	d, obs := testDelivery(t)
	obs.Argv[0] = "/totally/different/wrapper-argv0"
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelArgv] != DeliveryVerified {
		t.Fatalf("argv[0] legitimately differs across hops and is excluded: %+v", del)
	}
}

func TestDeliveryNoObservedIsUnknownNeverPartial(t *testing.T) {
	d, _ := testDelivery(t)
	del := EvaluateContextDelivery(d, nil)
	if del.Overall != DeliveryUnknown {
		t.Fatalf("an old bridge yields unknown, never partial: %+v", del)
	}
	for ch, v := range del.Channels {
		if v != DeliveryUnknown {
			t.Fatalf("every expected channel must be unknown (%s=%s): %+v", ch, v, del)
		}
	}
}

func TestDeliveryDroppedContextKeyIsPartial(t *testing.T) {
	d, obs := testDelivery(t)
	delete(obs.EnvValues, "CONTENT_SET") // wrapper forwarded managed vars only
	del := EvaluateContextDelivery(d, obs)
	if del.Overall != DeliveryOverallPartial {
		t.Fatalf("managed intact but context key missing is partial, never verified: %+v", del)
	}
	if del.Channels[DeliveryChannelManagedEnv] != DeliveryVerified {
		t.Fatalf("the managed channel did verify: %+v", del)
	}
	if del.Channels[DeliveryChannelContextEnv] != DeliveryUnknown {
		t.Fatalf("an unreported key leaves the channel unknown: %+v", del)
	}
}

func TestDeliveryWrongValueIsMismatched(t *testing.T) {
	d, obs := testDelivery(t)
	obs.EnvValues["CONTENT_SET"] = "vanilla-pack"
	del := EvaluateContextDelivery(d, obs)
	if del.Overall != DeliveryOverallPartial || del.Channels[DeliveryChannelContextEnv] != DeliveryMismatched {
		t.Fatalf("a differing value is an explicit mismatch: %+v", del)
	}
	if !strings.Contains(del.Reasons[DeliveryChannelContextEnv], "CONTENT_SET") {
		t.Fatalf("the failing key must be named: %+v", del.Reasons)
	}
}

func TestDeliveryReintroducedAbsentNameFailsChannel(t *testing.T) {
	d, obs := testDelivery(t)
	obs.EnvAbsent = nil
	obs.EnvValues["DEBUG_OVERLAY"] = "1" // a boundary reintroduced the unset name
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelContextEnv] != DeliveryMismatched {
		t.Fatalf("a reintroduced absent name fails exactly like a wrong value: %+v", del)
	}
	if del.Overall != DeliveryOverallPartial {
		t.Fatalf("expected partial: %+v", del)
	}
}

func TestDeliveryUnreportedAbsenceIsUnknown(t *testing.T) {
	d, obs := testDelivery(t)
	obs.EnvAbsent = nil // present keys reported, absence unreported
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelContextEnv] != DeliveryUnknown {
		t.Fatalf("unreported absence leaves the channel unknown: %+v", del)
	}
	if del.Overall != DeliveryOverallPartial {
		t.Fatalf("comparable evidence mixed with unknown is partial: %+v", del)
	}
}

func TestDeliveryOmittedEnvListsAreUnknown(t *testing.T) {
	d, obs := testDelivery(t)
	obs.EnvValues = nil
	obs.EnvAbsent = nil
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelManagedEnv] != DeliveryUnknown || del.Channels[DeliveryChannelContextEnv] != DeliveryUnknown {
		t.Fatalf("a welcome omitting both env lists leaves the env channels unknown: %+v", del)
	}
	if del.Overall != DeliveryOverallPartial {
		t.Fatalf("argv+cwd verified with env unknown is at most partial: %+v", del)
	}
}

func TestDeliveryUnverifiableCwdCapsAtPartial(t *testing.T) {
	d, err := ComputeContextDigests(
		[]string{"-x"}, "", true, // legacy relative workingDir: cannot be compared by contract
		map[string]string{"GABS_GAME_ID": "g"}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	obs := &ObservedContext{
		Argv:      []string{"bin", "-x"},
		Cwd:       "/anywhere",
		EnvValues: map[string]string{"GABS_GAME_ID": "g"},
	}
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelCwd] != DeliveryUnverifiable {
		t.Fatalf("legacy relative workingDir is unverifiable: %+v", del)
	}
	if del.Overall != DeliveryOverallPartial {
		t.Fatalf("everything else verified still caps at partial: %+v", del)
	}
}

func TestDeliveryCwdCanonicalization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink canonicalization cell runs on unix")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	d, err := ComputeContextDigests([]string{"-x"}, link, false, map[string]string{"GABS_GAME_ID": "g"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	obs := &ObservedContext{
		Argv:      []string{"bin", "-x"},
		Cwd:       real, // the workload reports the resolved path
		EnvValues: map[string]string{"GABS_GAME_ID": "g"},
	}
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelCwd] != DeliveryVerified {
		t.Fatalf("symlinked and resolved paths must canonicalize to the same digest: %+v", del)
	}

	// A path that cannot be canonicalized is unknown, never a false mismatch.
	obs.Cwd = filepath.Join(dir, "does-not-exist-anywhere")
	del = EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelCwd] != DeliveryUnknown {
		t.Fatalf("canonicalization failure is unknown: %+v", del)
	}
}

func TestDeliveryOnlyUnknownAndUnverifiableIsUnknown(t *testing.T) {
	d, err := ComputeContextDigests([]string{"-x"}, "", true, map[string]string{"GABS_GAME_ID": "g"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The report exists but carries nothing comparable.
	obs := &ObservedContext{}
	del := EvaluateContextDelivery(d, obs)
	if del.Overall != DeliveryUnknown {
		t.Fatalf("no comparable evidence and no mismatch is unknown: %+v", del)
	}
}

func TestDeliveryExpectedPresentPositivelyAbsentIsMismatch(t *testing.T) {
	d, obs := testDelivery(t)
	delete(obs.EnvValues, "CONTENT_SET")
	obs.EnvAbsent = append(obs.EnvAbsent, "CONTENT_SET") // positively checked and absent
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelContextEnv] != DeliveryMismatched {
		t.Fatalf("expected-present positively absent is a mismatch, not unknown: %+v", del)
	}
	if !strings.Contains(del.Reasons[DeliveryChannelContextEnv], "CONTENT_SET") {
		t.Fatalf("the failing key must be named: %+v", del.Reasons)
	}
}

func TestDeliveryContradictoryReportNeverVerifies(t *testing.T) {
	d, obs := testDelivery(t)
	obs.EnvAbsent = append(obs.EnvAbsent, "CONTENT_SET") // also present in envValues
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelContextEnv] != DeliveryMismatched {
		t.Fatalf("a key in both lists is contradictory and must not verify: %+v", del)
	}

	// The contradiction applies to absent-checked names too.
	d2, obs2 := testDelivery(t)
	obs2.EnvValues["DEBUG_OVERLAY"] = "1" // while also listed absent
	del2 := EvaluateContextDelivery(d2, obs2)
	if del2.Channels[DeliveryChannelContextEnv] != DeliveryMismatched {
		t.Fatalf("both-lists contradiction on an absent name must mismatch: %+v", del2)
	}
}

func TestDeliverySpawnCanonicalizationFailureIsUnknown(t *testing.T) {
	// The spawn-side cwd could not be canonicalized (nonexistent path):
	// distinct from the legacy-relative unverifiable case — the channel is
	// unknown per the binding rule (design/20).
	d, err := ComputeContextDigests([]string{"-x"}, "/definitely/not/a/real/path/anywhere", false,
		map[string]string{"GABS_GAME_ID": "g"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.CwdUnverifiable || d.CwdSHA256 != "" {
		t.Fatalf("a canonicalization failure pins neither a digest nor the unverifiable marker: %+v", d)
	}
	obs := &ObservedContext{
		Argv:      []string{"bin", "-x"},
		Cwd:       "/anywhere",
		EnvValues: map[string]string{"GABS_GAME_ID": "g"},
	}
	del := EvaluateContextDelivery(d, obs)
	if del.Channels[DeliveryChannelCwd] != DeliveryUnknown {
		t.Fatalf("spawn-side canonicalization failure is unknown, never unverifiable or mismatch: %+v", del)
	}
	if del.Overall != DeliveryOverallPartial {
		t.Fatalf("comparable evidence mixed with unknown is partial: %+v", del)
	}
}
