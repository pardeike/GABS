package main

import (
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/util"
)

const profileConfigNoMinVersion = `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","defaultProfile":"fast","profiles":{"fast":{"args":["--fast"]}}}}}`

// TestDoctorWarnsAboutSilentProfileDropRisk covers the one hazard GABS cannot
// enforce: a pre-1.1.0 binary reads a profiles config without complaint and
// drops every arg the profile contributes, launching against the wrong data
// root. No config construct makes those binaries fail — verified against the
// 1.0.8 release, which ignores unknown top-level fields AND a bumped config
// `version`. Since the risk cannot be blocked, doctor must at least name it.
func TestDoctorWarnsAboutSilentProfileDropRisk(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, profileConfigNoMinVersion)
	log := util.NewLogger("error")

	out := captureStdout(t, func() { runDoctor(log, "g", dir, false) })

	for _, want := range []string{"minGabsVersion", "1.1.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor must name %q when profiles are used without a declared minimum:\n%s", want, out)
		}
	}
}

// TestDoctorSilentWhenRequirementDeclared keeps the advisory from nagging once
// the author has acted on it.
func TestDoctorSilentWhenRequirementDeclared(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","minGabsVersion":"1.1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true","defaultProfile":"fast","profiles":{"fast":{"args":["--fast"]}}}}}`)
	log := util.NewLogger("error")

	out := captureStdout(t, func() { runDoctor(log, "g", dir, false) })

	if strings.Contains(out, "silently") {
		t.Fatalf("a declared requirement must not still warn about silent drops:\n%s", out)
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

	if strings.Contains(out, "minGabsVersion") {
		t.Fatalf("a legacy game must get no version advice:\n%s", out)
	}
}
