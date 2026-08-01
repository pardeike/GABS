package process

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Context-delivery channels and verdicts (design/03). The four channels are
// compared independently — argv, cwd, managed env, config-context env — and
// aggregated by the pinned matrix; a wrapper that forwards the managed
// variables but drops a context key yields partial, never a false verified.
const (
	DeliveryChannelArgv       = "argv"
	DeliveryChannelCwd        = "cwd"
	DeliveryChannelManagedEnv = "managedEnv"
	DeliveryChannelContextEnv = "contextEnv"

	DeliveryVerified       = "verified"
	DeliveryMismatched     = "mismatched"
	DeliveryUnknown        = "unknown"
	DeliveryUnverifiable   = "unverifiable"
	DeliveryOverallPartial = "partial"
)

// ObservedContext is the bridge's welcome-time delivery report as seen
// inside the game process (design/03): raw observed values — GABS hashes
// locally, compares, and discards. Env carries the values of the keys named
// in GABS_FORWARD_ENV; Absent lists the GABS_ABSENT_ENV names the bridge
// confirms absent. A key that was meant to be absent but appears in Env was
// reintroduced by a boundary and fails the channel like a wrong value.
type ObservedContext struct {
	Argv      []string
	Cwd       string
	EnvValues map[string]string
	EnvAbsent []string
}

// ComputeContextDigests pins the expected launch context at spawn as
// non-reversible salted digests (design/03, design/07): the argv payload
// excluding argv[0], the canonical cwd, and each forwarded env value —
// with channel membership persisted explicitly (managed versus context)
// because the managed layer includes non-prefixed names (SteamAppId,
// SystemRoot) and prefix guessing is not a persistable contract.
// Absent-env NAMES (never values) ride along for the isolation check.
// cwdUnverifiable marks the one contract-level incomparable case (legacy
// relative game-level workingDir); a spawn-side canonicalization FAILURE
// is different — the channel becomes unknown (no digest, no unverifiable
// marker), per the binding rule.
func ComputeContextDigests(argvPayload []string, cwd string, cwdUnverifiable bool, managedEnv, contextEnv map[string]string, absentNames []string) (*RuntimeContextDigests, error) {
	d := &RuntimeContextDigests{
		Salt:            NewFencingID(),
		CwdUnverifiable: cwdUnverifiable,
	}
	d.ArgvSHA256 = digestArgv(d.Salt, argvPayload)
	if !cwdUnverifiable {
		if canonical, err := CanonicalizeCwd(cwd); err == nil {
			d.CwdSHA256 = digestValue(d.Salt, canonical)
		}
		// else: no digest and no unverifiable marker — evaluation reports
		// the channel unknown ("spawn-side canonicalization failed").
	}
	if len(managedEnv) > 0 {
		d.ManagedEnvSHA256 = make(map[string]string, len(managedEnv))
		for k, v := range managedEnv {
			d.ManagedEnvSHA256[k] = digestValue(d.Salt, v)
		}
	}
	if len(contextEnv) > 0 {
		d.ContextEnvSHA256 = make(map[string]string, len(contextEnv))
		for k, v := range contextEnv {
			d.ContextEnvSHA256[k] = digestValue(d.Salt, v)
		}
	}
	if len(absentNames) > 0 {
		d.AbsentEnvNames = append([]string(nil), absentNames...)
		sort.Strings(d.AbsentEnvNames)
	}
	return d, nil
}

// ArgvPayloadForDigest returns the argv payload the WORKLOAD actually receives —
// what the argv channel must be digested against (design/03; T-DELIV). For the
// one documented Windows wrapper shape, `cmd.exe /c script.cmd <payload>`
// (design/01: batch files are configured EXPLICITLY as cmd.exe with /c ... args;
// GABS never implicitly wraps), the script re-launches the workload via %%*, so
// the workload sees only the tokens after the script — the /c flag and the
// script path are launch prefix, not payload. This refines design/20's "elements
// after argv[0]" to launch-prefix exclusion for that one shape, driven by
// T-DELIV's requirement (design/30) that the cmd.exe /c wrapper argv verify
// fully. Every other launch (DirectPath, unix wrapper-as-target) returns args
// unchanged, so no existing digest changes. cmd.exe re-quotes %%*, so exotic
// values may mis-split — already a documented caveat (design/03), not handled
// here.
func ArgvPayloadForDigest(pathOrId string, args []string) []string {
	// Separator-agnostic basename: a cmd.exe target carries Windows `\`
	// separators, but the digest may be computed off-Windows in tests, where
	// filepath.Base would not split them.
	base := pathOrId
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	if (base == "cmd" || base == "cmd.exe") && len(args) >= 2 && strings.EqualFold(args[0], "/c") {
		return args[2:]
	}
	return args
}

// CanonicalizeCwd produces the one platform-canonical form both sides are
// compared in (design/03): absolute, symlink-resolved, case- and
// separator-folded on Windows.
func CanonicalizeCwd(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty working directory")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(filepath.ToSlash(resolved))
	}
	return resolved, nil
}

func digestValue(salt, value string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

// digestArgv uses the pinned length-prefixed encoding (design/20): each
// element is preceded by its byte length, so element boundaries are
// unambiguous regardless of content. This digest is persisted runtime
// state — the algorithm must stay stable across restarts and binaries.
func digestArgv(salt string, payload []string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	var lenBuf [8]byte
	for _, a := range payload {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(a)))
		h.Write(lenBuf[:])
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// channelState accumulates per-member comparisons into one channel verdict:
// any mismatch wins, then any unknown, else verified.
type channelState struct {
	mismatch []string
	unknown  []string
	verified int
}

func (c *channelState) verdict() string {
	switch {
	case len(c.mismatch) > 0:
		return DeliveryMismatched
	case len(c.unknown) > 0:
		return DeliveryUnknown
	default:
		return DeliveryVerified
	}
}

func (c *channelState) reason() string {
	switch {
	case len(c.mismatch) > 0:
		return strings.Join(c.mismatch, "; ")
	case len(c.unknown) > 0:
		return strings.Join(c.unknown, "; ")
	default:
		return ""
	}
}

// EvaluateContextDelivery compares a welcome-time observation against the
// spawn-pinned digests and aggregates per the pinned matrix (design/03).
// Comparing against spawn-time digests — never current config — keeps the
// verdict correct for delayed handshakes after CLI starts, server restarts,
// and config edits.
func EvaluateContextDelivery(d *RuntimeContextDigests, obs *ObservedContext) *RuntimeContextDelivery {
	delivery := &RuntimeContextDelivery{
		Channels: map[string]string{},
		Reasons:  map[string]string{},
	}
	if d == nil {
		delivery.Overall = DeliveryUnknown
		delivery.Reasons["overall"] = "no expected-context digests were pinned at spawn"
		return delivery
	}

	expectArgv := d.ArgvSHA256 != ""
	// Every process has a working directory: the channel is always
	// expected; its digest state distinguishes comparable, legacy-relative
	// unverifiable, and spawn-side canonicalization failure (unknown).
	expectCwd := true
	expectManaged := len(d.ManagedEnvSHA256) > 0
	expectContext := len(d.ContextEnvSHA256) > 0 || len(d.AbsentEnvNames) > 0

	if obs == nil {
		// No observed field at all: an old bridge yields unknown for every
		// expected channel — never partial (design/03).
		reason := "the bridge reported no observed context (pre-report bridge)"
		if expectArgv {
			delivery.Channels[DeliveryChannelArgv] = DeliveryUnknown
			delivery.Reasons[DeliveryChannelArgv] = reason
		}
		if expectCwd {
			delivery.Channels[DeliveryChannelCwd] = DeliveryUnknown
			delivery.Reasons[DeliveryChannelCwd] = reason
		}
		if expectManaged {
			delivery.Channels[DeliveryChannelManagedEnv] = DeliveryUnknown
			delivery.Reasons[DeliveryChannelManagedEnv] = reason
		}
		if expectContext {
			delivery.Channels[DeliveryChannelContextEnv] = DeliveryUnknown
			delivery.Reasons[DeliveryChannelContextEnv] = reason
		}
		delivery.Overall = DeliveryUnknown
		return delivery
	}

	if expectArgv {
		if len(obs.Argv) == 0 {
			delivery.Channels[DeliveryChannelArgv] = DeliveryUnknown
			delivery.Reasons[DeliveryChannelArgv] = "argv not reported"
		} else if digestArgv(d.Salt, obs.Argv[1:]) == d.ArgvSHA256 {
			// The payload excludes argv[0]: element zero legitimately
			// differs across hops (design/03).
			delivery.Channels[DeliveryChannelArgv] = DeliveryVerified
		} else {
			delivery.Channels[DeliveryChannelArgv] = DeliveryMismatched
			delivery.Reasons[DeliveryChannelArgv] = "the observed argument payload differs from the resolved launch arguments"
		}
	}

	switch {
	case d.CwdUnverifiable:
		delivery.Channels[DeliveryChannelCwd] = DeliveryUnverifiable
		delivery.Reasons[DeliveryChannelCwd] = "the working directory cannot be compared by contract (legacy relative workingDir)"
	case d.CwdSHA256 == "":
		delivery.Channels[DeliveryChannelCwd] = DeliveryUnknown
		delivery.Reasons[DeliveryChannelCwd] = "the spawn-side working directory could not be canonicalized"
	case obs.Cwd == "":
		delivery.Channels[DeliveryChannelCwd] = DeliveryUnknown
		delivery.Reasons[DeliveryChannelCwd] = "working directory not reported"
	default:
		canonical, err := CanonicalizeCwd(obs.Cwd)
		if err != nil {
			// Canonicalization failure is unknown, never a false mismatch
			// (design/03).
			delivery.Channels[DeliveryChannelCwd] = DeliveryUnknown
			delivery.Reasons[DeliveryChannelCwd] = fmt.Sprintf("the reported working directory could not be canonicalized: %v", err)
		} else if digestValue(d.Salt, canonical) == d.CwdSHA256 {
			delivery.Channels[DeliveryChannelCwd] = DeliveryVerified
		} else {
			delivery.Channels[DeliveryChannelCwd] = DeliveryMismatched
			delivery.Reasons[DeliveryChannelCwd] = "the observed working directory differs from the resolved one"
		}
	}

	envReported := obs.EnvValues != nil || obs.EnvAbsent != nil
	managed := &channelState{}
	context := &channelState{}
	compareExpected := func(ch *channelState, digests map[string]string) {
		keys := make([]string, 0, len(digests))
		for k := range digests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !envReported {
				ch.unknown = append(ch.unknown, fmt.Sprintf("%s unreported (the welcome omits the env lists)", k))
				continue
			}
			v, present := obs.EnvValues[k]
			absent := containsString(obs.EnvAbsent, k)
			switch {
			case present && absent:
				// Contradictory report: never a pass.
				ch.mismatch = append(ch.mismatch, fmt.Sprintf("%s reported both present and absent (contradictory report)", k))
			case present:
				if digestValue(d.Salt, v) == digests[k] {
					ch.verified++
				} else {
					ch.mismatch = append(ch.mismatch, fmt.Sprintf("%s differs from the resolved value", k))
				}
			case absent:
				// Positively checked and absent while expected present: a
				// mismatch, not unknown (the binding three-state encoding).
				ch.mismatch = append(ch.mismatch, fmt.Sprintf("%s was expected present but is positively absent in the workload", k))
			default:
				ch.unknown = append(ch.unknown, fmt.Sprintf("%s unreported", k))
			}
		}
	}
	compareExpected(managed, d.ManagedEnvSHA256)
	compareExpected(context, d.ContextEnvSHA256)
	for _, n := range d.AbsentEnvNames {
		_, present := obs.EnvValues[n]
		absent := containsString(obs.EnvAbsent, n)
		switch {
		case !envReported:
			context.unknown = append(context.unknown, fmt.Sprintf("absence of %s unreported (the welcome omits the env lists)", n))
		case present && absent:
			context.mismatch = append(context.mismatch, fmt.Sprintf("%s reported both present and absent (contradictory report)", n))
		case present:
			// Meant to be absent, arrived with a value: a boundary
			// reintroduced it — fails exactly like a wrong value.
			context.mismatch = append(context.mismatch, fmt.Sprintf("%s was meant to be absent but is present in the workload", n))
		case absent:
			context.verified++
		default:
			context.unknown = append(context.unknown, fmt.Sprintf("absence of %s unreported", n))
		}
	}
	if expectManaged {
		delivery.Channels[DeliveryChannelManagedEnv] = managed.verdict()
		if r := managed.reason(); r != "" {
			delivery.Reasons[DeliveryChannelManagedEnv] = r
		}
	}
	if expectContext {
		delivery.Channels[DeliveryChannelContextEnv] = context.verdict()
		if r := context.reason(); r != "" {
			delivery.Reasons[DeliveryChannelContextEnv] = r
		}
	}

	// The pinned aggregation matrix (design/03).
	anyMismatch, anyUnknownish, anyVerified := false, false, false
	for _, v := range delivery.Channels {
		switch v {
		case DeliveryMismatched:
			anyMismatch = true
		case DeliveryUnknown, DeliveryUnverifiable:
			anyUnknownish = true
		case DeliveryVerified:
			anyVerified = true
		}
	}
	switch {
	case anyMismatch:
		delivery.Overall = DeliveryOverallPartial
	case anyVerified && anyUnknownish:
		delivery.Overall = DeliveryOverallPartial
	case anyVerified:
		delivery.Overall = DeliveryVerified
	default:
		delivery.Overall = DeliveryUnknown
	}
	return delivery
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
