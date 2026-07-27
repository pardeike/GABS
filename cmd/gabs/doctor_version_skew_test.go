package main

import (
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

const profileConfig = `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","defaultProfile":"fast","profiles":{"fast":{"args":["--fast"]}}}}}`

// TestDoctorWarnsAboutSilentProfileDropRisk covers the one hazard GABS cannot
// enforce: a pre-1.1.0 binary reads a profiles config without complaint and
// drops every arg the profile contributes, launching against the wrong data
// root. No config construct makes those binaries fail — verified against the
// 1.0.8 release, which ignores unknown top-level fields AND a bumped config
// `version`. Since the risk cannot be blocked, doctor must at least name it
// and tell the operator to upgrade every binary reading the config directory.
func TestDoctorWarnsAboutSilentProfileDropRisk(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, profileConfig)
	log := util.NewLogger("error")

	out := captureStdout(t, func() { runDoctor(log, "g", dir, false) })

	for _, want := range []string{"silently ignore", "1.1.0", "Upgrade every GABS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor must name %q when profiles are used:\n%s", want, out)
		}
	}
}

// TestDoctorVersionSkewAdvisoryIsNonFatal keeps the advisory advisory: naming
// the hazard must never flip an otherwise healthy report to failing.
func TestDoctorVersionSkewAdvisoryIsNonFatal(t *testing.T) {
	d := &doctorReport{healthy: true}
	game := &config.GameConfig{Profiles: map[string]config.ProfileConfig{"fast": {}}}

	captureStdout(t, func() { doctorVersionSkew(d, game) })

	if !d.healthy {
		t.Fatal("version-skew advisory must not flip doctor health")
	}
}

// TestDoctorWarnsForEnvOnlyGame covers the same silent-drop hazard for the
// remaining 1.1 launch-context fields: a game using only game-level env (or
// unsetEnv) loses its environment to a pre-1.1 binary exactly like
// profile-contributed arguments, so it must receive the same advisory.
func TestDoctorWarnsForEnvOnlyGame(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","env":{"DATA_ROOT":"/srv/data"}}}}`)
	log := util.NewLogger("error")

	out := captureStdout(t, func() { runDoctor(log, "g", dir, false) })

	if !strings.Contains(out, "silently ignore") {
		t.Fatalf("an env-only game must get the version-skew advisory:\n%s", out)
	}
}

// TestDoctorNoVersionAdviceForLegacyGames protects the compatibility promise: a
// game with no profiles, inputs or hooks is readable by any GABS, so there is
// nothing to advise about.
func TestDoctorNoVersionAdviceForLegacyGames(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true"}}}`)
	log := util.NewLogger("error")

	out := captureStdout(t, func() { runDoctor(log, "g", dir, false) })

	if strings.Contains(out, "silently ignore") {
		t.Fatalf("a legacy game must get no version advice:\n%s", out)
	}
}
