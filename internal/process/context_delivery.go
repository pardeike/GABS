package process

import (
	"crypto/sha256"
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
	Argv   []string
	Cwd    string
	Env    map[string]string
	Absent []string
}

// ComputeContextDigests pins the expected launch context at spawn as
// non-reversible salted digests (design/03, design/07): the argv payload
// excluding argv[0], the canonical cwd, and each forwarded env value.
// Absent-env NAMES (never values) ride along for the isolation check.
// cwdUnverifiable marks the one contract-level incomparable case (legacy
// relative game-level workingDir).
func ComputeContextDigests(argvPayload []string, cwd string, cwdUnverifiable bool, forwardEnv map[string]string, absentNames []string) (*RuntimeContextDigests, error) {
	d := &RuntimeContextDigests{
		Salt:            NewFencingID(),
		CwdUnverifiable: cwdUnverifiable,
	}
	d.ArgvSHA256 = digestArgv(d.Salt, argvPayload)
	if !cwdUnverifiable {
		canonical, err := CanonicalizeCwd(cwd)
		if err != nil {
			// A spawn-side canonicalization failure makes the channel
			// unverifiable rather than pinning a digest that could only
			// produce false mismatches.
			d.CwdUnverifiable = true
		} else {
			d.CwdSHA256 = digestValue(d.Salt, canonical)
		}
	}
	if len(forwardEnv) > 0 {
		d.EnvSHA256 = make(map[string]string, len(forwardEnv))
		for k, v := range forwardEnv {
			d.EnvSHA256[k] = digestValue(d.Salt, v)
		}
	}
	if len(absentNames) > 0 {
		d.AbsentEnvNames = append([]string(nil), absentNames...)
		sort.Strings(d.AbsentEnvNames)
	}
	return d, nil
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

func digestArgv(salt string, payload []string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	for _, a := range payload {
		h.Write([]byte{0})
		h.Write([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// managedEnvName classifies a forwarded key into the managed-env channel
// (the GABS/GABP variables) versus the config-context channel.
func managedEnvName(name string) bool {
	return strings.HasPrefix(name, "GABS_") || strings.HasPrefix(name, "GABP_")
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
	expectCwd := d.CwdSHA256 != "" || d.CwdUnverifiable
	expectManaged := false
	expectContext := len(d.AbsentEnvNames) > 0
	for k := range d.EnvSHA256 {
		if managedEnvName(k) {
			expectManaged = true
		} else {
			expectContext = true
		}
	}

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

	if expectCwd {
		switch {
		case d.CwdUnverifiable:
			delivery.Channels[DeliveryChannelCwd] = DeliveryUnverifiable
			delivery.Reasons[DeliveryChannelCwd] = "the working directory cannot be compared by contract (legacy relative workingDir)"
		case obs.Cwd == "":
			delivery.Channels[DeliveryChannelCwd] = DeliveryUnknown
			delivery.Reasons[DeliveryChannelCwd] = "working directory not reported"
		default:
			canonical, err := CanonicalizeCwd(obs.Cwd)
			if err != nil {
				// Canonicalization failure is unknown, never a false
				// mismatch (design/03).
				delivery.Channels[DeliveryChannelCwd] = DeliveryUnknown
				delivery.Reasons[DeliveryChannelCwd] = fmt.Sprintf("the reported working directory could not be canonicalized: %v", err)
			} else if digestValue(d.Salt, canonical) == d.CwdSHA256 {
				delivery.Channels[DeliveryChannelCwd] = DeliveryVerified
			} else {
				delivery.Channels[DeliveryChannelCwd] = DeliveryMismatched
				delivery.Reasons[DeliveryChannelCwd] = "the observed working directory differs from the resolved one"
			}
		}
	}

	envReported := obs.Env != nil || obs.Absent != nil
	managed := &channelState{}
	context := &channelState{}
	keys := make([]string, 0, len(d.EnvSHA256))
	for k := range d.EnvSHA256 {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ch := context
		if managedEnvName(k) {
			ch = managed
		}
		if !envReported {
			ch.unknown = append(ch.unknown, fmt.Sprintf("%s unreported (the welcome omits the env lists)", k))
			continue
		}
		v, ok := obs.Env[k]
		if !ok {
			ch.unknown = append(ch.unknown, fmt.Sprintf("%s unreported", k))
			continue
		}
		if digestValue(d.Salt, v) == d.EnvSHA256[k] {
			ch.verified++
		} else {
			ch.mismatch = append(ch.mismatch, fmt.Sprintf("%s differs from the resolved value", k))
		}
	}
	for _, n := range d.AbsentEnvNames {
		switch {
		case !envReported:
			context.unknown = append(context.unknown, fmt.Sprintf("absence of %s unreported (the welcome omits the env lists)", n))
		case obs.Env[n] != "" || func() bool { _, present := obs.Env[n]; return present }():
			// Meant to be absent, arrived with a value: a boundary
			// reintroduced it — fails exactly like a wrong value.
			context.mismatch = append(context.mismatch, fmt.Sprintf("%s was meant to be absent but is present in the workload", n))
		case containsString(obs.Absent, n):
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
