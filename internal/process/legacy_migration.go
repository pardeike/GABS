package process

import (
	"fmt"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/config"
)

// NormalizeLegacyClaim performs the one-time full normalization of a
// pre-profile claim on its first lifecycle touch — connect, stop, kill, or
// a start's duplicate check; never read-only status (design/07). Under the
// transition lock it stamps the schema marker, mints the launch identity
// (fencing is valid from then on), sets phase active and the profile
// unprofiled, pins the built-in fallback from the legacy claim's own
// stopProcessName and PID (no fingerprint exists, so the PID remains weak
// evidence), and — the single recorded exception to never-consult-config —
// takes the launch mode and PID role from the current entry, recording
// normalizedFromLegacy plus the revision used. The discriminator is exact
// marker absence (SchemaVersion == 0): any other marker version is a
// different schema, never legacy. Idempotent: a marker-stamped claim is
// returned unchanged.
func NormalizeLegacyClaim(gameID, configDir, currentLaunchMode, currentConfigRevision string) (*RuntimeState, error) {
	claim, _, err := normalizeLegacyClaim(gameID, configDir, currentLaunchMode, currentConfigRevision, false)
	return claim, err
}

// NormalizeLegacyClaimCapturingEndpoint is the games_connect variant of the
// first lifecycle touch: while STILL holding the transition lock over a
// marker-absent claim, it captures the one legacy bridge.json endpoint
// candidate (design/07: the sole live-attach read of the file — under the
// lock, exactly once). The caller validates the candidate by actually
// connecting, then persists it only through the minted launch fence. There
// is no re-read: an already-stamped claim yields no candidate, ever.
func NormalizeLegacyClaimCapturingEndpoint(gameID, configDir, currentLaunchMode, currentConfigRevision string) (*RuntimeState, *RuntimeEndpoint, error) {
	return normalizeLegacyClaim(gameID, configDir, currentLaunchMode, currentConfigRevision, true)
}

func normalizeLegacyClaim(gameID, configDir, currentLaunchMode, currentConfigRevision string, captureEndpoint bool) (*RuntimeState, *RuntimeEndpoint, error) {
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return nil, nil, err
	}
	defer lock.Release()

	cur, err := loadRuntimeStateLocked(gameID, configDir)
	if err != nil {
		return nil, nil, err
	}
	if cur == nil {
		return nil, nil, fmt.Errorf("%w for %s", ErrNoRuntimeClaim, gameID)
	}
	if cur.SchemaVersion != 0 {
		return cur, nil, nil // marker present: never the migration path
	}

	var candidate *RuntimeEndpoint
	if captureEndpoint {
		// The one legacy candidate, read while the lock is held. A failed
		// later validation does not reopen this window — the marker is
		// stamped below in this same touch.
		if _, port, token, rerr := config.ReadBridgeJSON(gameID, configDir); rerr == nil && port > 0 && strings.TrimSpace(token) != "" {
			candidate = &RuntimeEndpoint{Port: port, Token: token}
		}
	}

	cur.SchemaVersion = RuntimeSchemaVersion
	cur.LaunchID = NewFencingID()
	if cur.Generation == 0 {
		cur.Generation = 1
	} else {
		cur.Generation++
	}
	cur.Phase = PhaseActive
	cur.Source = SourceGABS
	cur.Profile = ""
	cur.LaunchMode = currentLaunchMode
	if currentLaunchMode == "SteamAppId" || currentLaunchMode == "EpicAppId" {
		cur.PIDRole = PIDRoleHelper
	} else {
		cur.PIDRole = PIDRoleWorkload
	}
	cur.PIDStartTime = 0 // no fingerprint was ever recorded: weak evidence
	cur.BuiltinFallback = pinBuiltinFallback()
	// A pre-profile GABS launch used no declared launch inputs — a known
	// empty set, not the external-snapshot "unavailable" state.
	cur.AppliedInputsState = ""
	cur.AppliedInputNames = nil
	cur.NormalizedFromLegacy = true
	cur.ConfigRevision = currentConfigRevision
	cur.UpdatedAt = time.Now().UTC()

	if err := SaveRuntimeState(gameID, configDir, *cur); err != nil {
		return nil, nil, err
	}
	return cur, candidate, nil
}
