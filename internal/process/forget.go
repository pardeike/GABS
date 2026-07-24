package process

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/pardeike/gabs/internal/config"
)

// Force-forget is the design/07 escape hatch: it removes a runtime claim a
// normal fenced removal will not, bypassing LIVENESS — but never claim identity
// or credit reconciliation. These sentinels let the CLI drive the two-phase
// confirmation the reviewer requires (round 19).
var (
	// ErrForgetClaimChanged means the on-disk claim no longer matches the bytes
	// whose evidence the user was shown (a server published a successor). The
	// caller must re-show the new evidence rather than delete an unseen claim.
	ErrForgetClaimChanged = errors.New("the runtime claim changed since its evidence was shown")
	// ErrForgetPendingUnreconciled means a readable claim's pending history
	// credits could not be committed (a history-write failure), so removing now
	// would silently lose them — allowed only with an explicit discard.
	ErrForgetPendingUnreconciled = errors.New("pending history credits could not be reconciled")
	// ErrForgetCorruptClaim means the claim is unreadable, so its pending facts
	// cannot be reconciled at all — removal is a discard.
	ErrForgetCorruptClaim = errors.New("the runtime claim is unreadable")
)

// ReadRuntimeClaim reads the exact runtime.json bytes for gameID ONCE and returns
// them with a stable content digest — the identity the forget flow binds its
// confirmation to (it works for a corrupt claim, since it digests raw bytes).
// found is false when there is no claim. The path is validated (design/07): a
// traversal/symlink-escaping ID returns an error, never a read outside the base.
func ReadRuntimeClaim(gameID, configDir string) (data []byte, digest string, found bool, err error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, "", false, err
	}
	path, err := cp.SafeRuntimeStatePath(gameID)
	if err != nil {
		return nil, "", false, err
	}
	data, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), true, nil
}

// ParseRuntimeClaim parses claim bytes; a nil error with a non-nil state means
// the claim is readable (the forget flow renders its evidence from these exact
// bytes, so evidence and the confirmed identity agree).
func ParseRuntimeClaim(data []byte) (*RuntimeState, error) {
	return parseRuntimeState(data)
}

// ForceForgetRuntimeClaim removes a runtime claim under the per-game transition
// lock, bypassing liveness/fencing but NOT claim identity or credit
// reconciliation (design/07 escape hatch, round-19 correction):
//   - It rereads the claim under the lock; if expectedDigest is non-empty and no
//     longer matches, it refuses with ErrForgetClaimChanged so the caller shows
//     the successor's evidence instead of deleting an unseen claim.
//   - A readable claim first has its pending clean-stop/delivery credits
//     reconciled (creditPendingThenRemoveLocked credits→removes→GCs); this
//     preserves the F5 "a pending fact survives until credited or explicitly
//     discarded" invariant when history is healthy.
//   - If that reconciliation fails (a history-write outage) or the claim is
//     corrupt, the pending facts cannot be preserved: removal proceeds only when
//     discardPending is set, which the caller sets after showing the loss and
//     taking an explicit second confirmation.
func ForceForgetRuntimeClaim(gameID, configDir, expectedDigest string, discardPending bool) error {
	lock, err := AcquireTransitionLock(gameID, configDir, transitionLockGateTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()

	_, digest, found, err := ReadRuntimeClaim(gameID, configDir)
	if err != nil {
		return err
	}
	if !found {
		return nil // already gone: the goal state
	}
	if expectedDigest != "" && digest != expectedDigest {
		return ErrForgetClaimChanged
	}

	cur, loadErr := LoadRuntimeState(gameID, configDir)
	if loadErr == nil && cur != nil {
		// Readable: credit the pending facts, then remove. A history-write
		// failure persists the claim and aborts (the reconcile-failed signal).
		if err := creditPendingThenRemoveLocked(gameID, configDir, cur); err != nil {
			if !discardPending {
				return fmt.Errorf("%w: %v", ErrForgetPendingUnreconciled, err)
			}
			return RemoveRuntimeState(gameID, configDir)
		}
		return nil
	}
	// Corrupt/unreadable: nothing to reconcile.
	if !discardPending {
		return ErrForgetCorruptClaim
	}
	return RemoveRuntimeState(gameID, configDir)
}
