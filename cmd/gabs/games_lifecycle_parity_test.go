package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/lifecycle"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// Every cliStartFailureText branch maps to a stable code from the exhaustive
// list — and never the invented "bridge_endpoint_in_use".
func TestCLIStartFailureTextExhaustive(t *testing.T) {
	game := config.GameConfig{ID: "g", Name: "G"}
	cases := []struct {
		name   string
		err    error
		code   string
		msgHas string
	}{
		{"refusal", &lifecycle.StartRefusalError{Refusal: &process.StartRefusal{Code: "already_running", Message: "m"}}, "already_running", ""},
		{"unobserved", &lifecycle.UnobservedStartError{}, "unobserved", ""},
		{"exited", &lifecycle.ExitedDuringStartError{ExitCode: 3}, "exited_during_start", "exit code 3"},
		{"active", &lifecycle.GameAlreadyActiveError{Status: process.RuntimeStateStatusRunning}, "already_running", ""},
		{"store-readiness", &lifecycle.StoreClientNotReadyError{Store: "steam", Reason: "readiness_timeout", Stage: "global_user", Retryable: true}, "store_client_not_ready", "global_user"},
		{"cache-collision", &lifecycle.EndpointUnavailableError{GameID: "g", Err: &config.BridgeEndpointInUseError{Port: 8080}}, "endpoint_unavailable", "endpoint_cache_in_use"},
		{"endpoint-generic", &lifecycle.EndpointUnavailableError{GameID: "g", Err: errors.New("disk full")}, "endpoint_unavailable", ""},
		{"spec-too-large", &launch.SpecSizeIssue{Message: "too big", Part: "argv"}, "spec_too_large", ""},
		{"fencing", process.ErrFencingViolation, "operation_in_progress", ""},
		{"spawn", &process.ProcessError{Type: process.ProcessErrorTypeStart, Err: errors.New("no such file")}, "spawn_failed", ""},
		{"unknown", errors.New("weird internal"), "blocked_unknown_state", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, msg := cliStartFailureText(c.err, game)
			if code == "bridge_endpoint_in_use" {
				t.Fatal("bridge_endpoint_in_use is not a permitted stable code")
			}
			if code != c.code {
				t.Fatalf("code = %q, want %q (msg: %s)", code, c.code, msg)
			}
			if c.msgHas != "" && !strings.Contains(msg, c.msgHas) {
				t.Fatalf("msg %q must contain %q", msg, c.msgHas)
			}
		})
	}
}

// A CLI stop/kill that first-touches a schema-0 (legacy) claim must normalize it
// with the ACTUAL snapshot revision, not an empty string (design/07).
func TestCLIStopNormalizesLegacyClaimWithRevision(t *testing.T) {
	dir := t.TempDir()
	writeCLIConfig(t, dir, `{"version":"1.0","games":{"g":{"id":"g","name":"G","launchMode":"DirectPath","target":"/bin/true"}}}`)
	snap, err := loadCLISnapshot(dir)
	if err != nil || snap.Revision == "" {
		t.Fatalf("snapshot revision must be non-empty: rev=%q err=%v", snap.Revision, err)
	}

	st := process.NewRuntimeState(process.LaunchSpec{GameId: "g", Mode: "DirectPath", PathOrId: "/bin/true"}, process.RuntimeStateStatusRunning)
	st.SchemaVersion = 0 // legacy claim
	st.Phase = process.PhaseActive
	if err := process.ClaimRuntimeState("g", dir, st); err != nil {
		t.Fatal(err)
	}

	// The CLI stop path derives mode + revision from ONE snapshot, then
	// LoadStopClaim normalizes the legacy claim.
	m := cliClaimManager(util.NewLogger("error"), dir)
	claim, err := m.LoadStopClaim("g", "DirectPath", snap.Revision)
	if err != nil || claim == nil {
		t.Fatalf("LoadStopClaim: claim=%v err=%v", claim, err)
	}
	if claim.ConfigRevision != snap.Revision {
		t.Fatalf("normalized claim revision = %q, want the snapshot revision %q (never empty)", claim.ConfigRevision, snap.Revision)
	}
}
