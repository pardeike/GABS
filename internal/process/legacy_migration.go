package process

import (
	"fmt"
	"time"
)

// NormalizeLegacyClaim performs the one-time full normalization of a
// pre-profile (schema-0) claim on its first lifecycle touch — connect,
// stop, kill, or a start's duplicate check; never read-only status
// (design/07). Under the transition lock it stamps the schema marker,
// mints the launch identity (fencing is valid from then on), sets phase
// active and the profile unprofiled, pins the built-in fallback from the
// legacy claim's own stopProcessName and PID (no fingerprint exists, so
// the PID remains weak evidence), and — the single recorded exception to
// never-consult-config — takes the launch mode and PID role from the
// current entry, recording normalizedFromLegacy plus the revision used.
// Idempotent: a marker-stamped claim is returned unchanged.
func NormalizeLegacyClaim(gameID, configDir, currentLaunchMode, currentConfigRevision string) (*RuntimeState, error) {
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	cur, err := LoadRuntimeState(gameID, configDir)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("%w for %s", ErrNoRuntimeClaim, gameID)
	}
	if cur.SchemaVersion >= RuntimeSchemaVersion {
		return cur, nil
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
	// A pre-profile launch's inputs are unknowable — like an external
	// snapshot, never an empty list that reads as launched-without-inputs.
	cur.AppliedInputsState = AppliedInputsStateUnavailable
	cur.NormalizedFromLegacy = true
	cur.ConfigRevision = currentConfigRevision
	cur.UpdatedAt = time.Now().UTC()

	if err := SaveRuntimeState(gameID, configDir, *cur); err != nil {
		return nil, err
	}
	return cur, nil
}
