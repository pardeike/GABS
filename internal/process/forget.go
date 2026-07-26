package process

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Force-forget is the design/07 escape hatch: it removes a runtime claim a
// normal fenced removal will not, bypassing LIVENESS — but never claim identity
// or credit reconciliation. These sentinels let the CLI drive the required
// two-phase confirmation.
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
// A claim that exists but cannot yield bytes — a non-regular leaf, which is
// never read through, or a regular file the caller lacks permission to read —
// is reported found with nil data (rendered as unreadable) and a digest of the
// leaf's own identity, so it can still be forgotten under the corrupt-claim
// discard flow.
func ReadRuntimeClaim(gameID, configDir string) (data []byte, digest string, found bool, err error) {
	path, gateInfo, err := regularClaimPath(gameID, configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", false, nil
		}
		if errors.Is(err, errNonRegularClaim) {
			digest, err := nonRegularClaimDigest(gameID, configDir)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil, "", false, nil
				}
				return nil, "", false, err
			}
			return nil, digest, true, nil
		}
		return nil, "", false, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, nil
		}
		if os.IsPermission(err) {
			// A regular claim that cannot be read (mode 000, an ACL denial)
			// must stay forgettable: the design promises repair can discard
			// what it cannot reconcile. Its identity is the leaf's observable
			// metadata, so a replacement mid-prompt still trips the
			// claim-changed guard.
			return nil, unreadableClaimDigest(gateInfo), true, nil
		}
		return nil, "", false, err
	}
	defer f.Close()
	// The open handle, not the pathname, is the read target: prove it is the
	// exact regular file the gate inspected, so a swap to a symlink between
	// gate and open is never read through and the shown evidence is never
	// bound to different bytes.
	handleInfo, err := f.Stat()
	if err != nil {
		return nil, "", false, err
	}
	if !handleInfo.Mode().IsRegular() || !os.SameFile(gateInfo, handleInfo) {
		return nil, "", false, fmt.Errorf("runtime claim %s changed while it was read", path)
	}
	data, err = io.ReadAll(f)
	if err != nil {
		if os.IsPermission(err) {
			return nil, unreadableClaimDigest(gateInfo), true, nil
		}
		return nil, "", false, err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), true, nil
}

// unreadableClaimDigest binds the discard confirmation for a permission-
// unreadable regular claim to the leaf's observable identity: mode, size and
// modification time. Any replacement or edit changes at least one of them and
// trips the claim-changed guard just like edited bytes would.
func unreadableClaimDigest(info os.FileInfo) string {
	identity := fmt.Sprintf("unreadable\x00%s\x00%d\x00%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

// nonRegularClaimDigest derives a confirmation identity for a claim leaf that
// must not be read through: a symlink is identified by its own target path
// (via Readlink, which never follows), any other special file by its mode. A
// replacement mid-prompt therefore changes the digest and trips the
// claim-changed guard just like edited bytes would.
func nonRegularClaimDigest(gameID, configDir string) (string, error) {
	parent, err := safeExactClaimParent(gameID, configDir)
	if err != nil {
		return "", err
	}
	leaf := filepath.Join(parent, "runtime.json")
	info, err := os.Lstat(leaf)
	if err != nil {
		return "", err
	}
	identity := "mode\x00" + info.Mode().String()
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(leaf)
		if err != nil {
			return "", err
		}
		identity = "symlink\x00" + target
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:]), nil
}

// ParseRuntimeClaim parses claim bytes; a nil error with a non-nil state means
// the claim is readable (the forget flow renders its evidence from these exact
// bytes, so evidence and the confirmed identity agree).
func ParseRuntimeClaim(data []byte) (*RuntimeState, error) {
	return parseRuntimeState(data)
}

// ForceForgetRuntimeClaim removes a runtime claim under the per-game transition
// lock, bypassing liveness/fencing but NOT claim identity or credit
// reconciliation (design/07 escape hatch):
//   - It rereads the claim under the lock; if expectedDigest is non-empty and no
//     longer matches, it refuses with ErrForgetClaimChanged so the caller shows
//     the successor's evidence instead of deleting an unseen claim.
//   - A readable claim first has its pending clean-stop/delivery credits
//     reconciled (creditPendingThenRemoveLocked credits→removes→GCs); this
//     preserves the "a pending fact survives until credited or explicitly
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
