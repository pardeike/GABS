package process

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testDigestsWithCwd(t *testing.T, cwd string) *RuntimeContextDigests {
	t.Helper()
	d, err := ComputeContextDigests(
		[]string{"-profile", "combat", "-scenario", "arena"},
		cwd, false,
		map[string]string{
			"GABP_SERVER_PORT": "43210",
			"GABP_TOKEN":       "secret-token",
			"GABS_GAME_ID":     "adventure",
			"CONTENT_SET":      "combat-pack",
		},
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
		Env: map[string]string{
			"GABP_SERVER_PORT": "43210",
			"GABP_TOKEN":       "secret-token",
			"GABS_GAME_ID":     "adventure",
			"CONTENT_SET":      "combat-pack",
		},
		Absent: []string{"DEBUG_OVERLAY"},
	}
}

func TestContextDigestsAreSaltedAndValueFree(t *testing.T) {
	cwd := t.TempDir()
	d1 := testDigestsWithCwd(t, cwd)
	d2 := testDigestsWithCwd(t, cwd)
	if d1.Salt == d2.Salt {
		t.Fatal("each launch mints its own salt")
	}
	if d1.ArgvSHA256 == d2.ArgvSHA256 || d1.EnvSHA256["GABP_TOKEN"] == d2.EnvSHA256["GABP_TOKEN"] {
		t.Fatal("digests must be salted: identical values must not produce identical digests across launches")
	}
	for k, v := range d1.EnvSHA256 {
		if strings.Contains(v, "secret-token") || strings.Contains(v, "combat-pack") {
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
	delete(obs.Env, "CONTENT_SET") // wrapper forwarded managed vars only
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
	obs.Env["CONTENT_SET"] = "vanilla-pack"
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
	obs.Absent = nil
	obs.Env["DEBUG_OVERLAY"] = "1" // a boundary reintroduced the unset name
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
	obs.Absent = nil // present keys reported, absence unreported
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
	obs.Env = nil
	obs.Absent = nil
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
		map[string]string{"GABS_GAME_ID": "g"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	obs := &ObservedContext{
		Argv: []string{"bin", "-x"},
		Cwd:  "/anywhere",
		Env:  map[string]string{"GABS_GAME_ID": "g"},
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

	d, err := ComputeContextDigests([]string{"-x"}, link, false, map[string]string{"GABS_GAME_ID": "g"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	obs := &ObservedContext{
		Argv: []string{"bin", "-x"},
		Cwd:  real, // the workload reports the resolved path
		Env:  map[string]string{"GABS_GAME_ID": "g"},
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
	d, err := ComputeContextDigests([]string{"-x"}, "", true, map[string]string{"GABS_GAME_ID": "g"}, nil)
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
