package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/steam"
	"github.com/pardeike/gabs/internal/util"
	"github.com/pardeike/gabs/internal/version"
)

// knownConflatingStatusTools are status-hook commands whose exit codes conflate
// "stopped" with "cannot determine" (design/01): a raw `docker inspect` exits 1
// both when the container is absent and when the daemon is unreachable, so a
// daemon hiccup reads as stopped and unblocks a duplicate start. The lint is
// advisory and basename-matched (design/20).
var knownConflatingStatusTools = map[string]bool{
	"docker": true, "docker.exe": true,
	"podman": true, "podman.exe": true,
}

// doctorReport accumulates findings; a hard failure flips healthy, an advisory
// warning does not. Doctor always runs every section and exits once at the end.
type doctorReport struct{ healthy bool }

func (d *doctorReport) info(format string, a ...interface{}) { fmt.Printf(format+"\n", a...) }
func (d *doctorReport) warn(format string, a ...interface{}) { fmt.Printf("  ! "+format+"\n", a...) }
func (d *doctorReport) fail(format string, a ...interface{}) {
	d.healthy = false
	fmt.Printf(format+"\n", a...)
}

// runDoctor is the profile-aware `gabs games doctor <id>` (design/11). It
// reports every diagnostic it can — a failed check never short-circuits the
// rest — and prints the full track record unconditionally (readable from
// history.json regardless of config/Steam/target state). --show-last-good adds
// the last-known-good entry so a human can compare or restore by hand.
func runDoctor(log util.Logger, gameID, configDir string, showLastGood bool) int {
	d := &doctorReport{healthy: true}

	// Loading the snapshot validates the config's profiles/inputs/hook
	// references (design/01); a ValidationError names the exact JSON paths.
	snap, snapErr := loadCLISnapshot(configDir)
	if snapErr != nil {
		d.fail("Configuration: invalid")
		for _, line := range strings.Split(strings.TrimSpace(snapErr.Error()), "\n") {
			d.info("  %s", line)
		}
	}

	var game *config.GameConfig
	if snap != nil {
		if g, ok := snap.Config.GetGame(gameID); ok {
			g.ID = gameID // the map key is the id; backfill for a hand-written config
			game = g
		}
	}

	if game == nil {
		d.fail("Game '%s': not found in configuration", gameID)
	} else {
		d.info("Game: %s", game.ID)
		d.info("Launch Mode: %s", game.LaunchMode)
		if game.Target != "" {
			d.info("Target: %s", game.Target)
		}
		if snapErr == nil {
			d.info("Configuration: valid")
		}
		doctorLaunchTarget(d, game)
		if snap != nil {
			doctorProfilesAndHooks(d, snap, game)
			doctorVersionSkew(d, game)
		}
		doctorMacOSTarget(d, game)
	}

	doctorPermissions(d, configDir, gameID)
	doctorTrackRecord(configDir, gameID)
	if showLastGood {
		doctorLastGood(log, configDir, gameID, snap, game)
	}

	if !d.healthy {
		return 1
	}
	return 0
}

// firstProfileAwareRelease is the first GABS release that understands
// `profiles`, `launchInputs`, and `lifecycle`.
const firstProfileAwareRelease = "1.1.0"

// doctorVersionSkew reports the one hazard GABS cannot enforce.
//
// A pre-1.1.0 GABS reads a config with `profiles`/`launchInputs`/`lifecycle`
// without complaint and ignores those fields, so every arg a profile
// contributes silently disappears and the workload launches against whatever
// data root the bare game-level args select. Verified against the 1.0.8
// release: it ignores unknown top-level fields AND a bumped config `version`,
// so no config construct makes an already-released binary refuse the file. The
// only available protection is telling the operator to upgrade every binary
// that reads this config directory, at the one place they ask for diagnostics.
func doctorVersionSkew(d *doctorReport, game *config.GameConfig) {
	usesNewFields := len(game.Profiles) > 0 || len(game.LaunchInputs) > 0 || game.Lifecycle != nil
	if !usesNewFields {
		return
	}

	d.warn("this game uses profiles/launch inputs/lifecycle hooks, which GABS releases before %s "+
		"silently ignore: an older binary reads this config without complaint and drops every "+
		"argument a profile contributes, so a launch can hit the wrong data root with nothing "+
		"logged. Upgrade every GABS that reads this config directory to %s or newer (this binary "+
		"is %s). Releases before %s cannot detect this and will not warn.",
		firstProfileAwareRelease, firstProfileAwareRelease, version.Get(), firstProfileAwareRelease)
}

// doctorLaunchTarget checks the launch target reachability per mode (the
// existing readiness checks, restructured to findings).
func doctorLaunchTarget(d *doctorReport, game *config.GameConfig) {
	switch game.LaunchMode {
	case "SteamAppId":
		d.info("Steam launch: launcher URL mode")
		d.info("Bridge environment: not guaranteed on the real game process")
		app, err := steam.ResolveApp(game.Target)
		if err != nil {
			d.fail("Managed Steam readiness: failed (%v)", err)
			return
		}
		printSteamAppResolution(app)
		d.info("Recommended repair: gabs games repair %s", game.ID)
	case "SteamManaged":
		d.info("Steam launch: managed executable mode")
		app, err := steam.ResolveApp(game.Target)
		if err != nil {
			d.fail("Managed Steam readiness: failed (%v)", err)
			return
		}
		printSteamAppResolution(app)
		ok, content, err := steam.CheckAppIDFile(app)
		switch {
		case err != nil:
			d.fail("Steam app id file: unreadable (%v)", err)
		case ok:
			d.info("Steam app id file: ready (%s)", app.AppIDFilePath)
		case content == "":
			d.fail("Steam app id file: missing (%s)", app.AppIDFilePath)
			d.info("Recommended repair: gabs games repair %s", game.ID)
		default:
			d.fail("Steam app id file: wrong id %q at %s", content, app.AppIDFilePath)
		}
	default:
		// Target existence/resolvability is reported per launchable context by
		// doctorProfilesAndHooks, which runs the same Stage-1 resolver check.
		// That correctly handles a PATH-resolved bare target and a
		// workingDir-relative target (e.g. ./run.sh); a raw os.Stat(game.Target)
		// here would falsely mark both "not found" and then contradict the
		// resolver loop's "resolves" finding.
	}
}

// doctorMacOSTarget implements the macOS doctor contract (design/20): warn when
// the launch target carries the com.apple.quarantine attribute (Gatekeeper may
// block it) and when a relative target risks App Translocation. No-op off macOS.
func doctorMacOSTarget(d *doctorReport, game *config.GameConfig) {
	if runtime.GOOS != "darwin" || game.Target == "" {
		return
	}
	if game.LaunchMode != "DirectPath" && game.LaunchMode != "" && game.LaunchMode != "CustomCommand" {
		return // URL/managed modes do not exec a path target directly
	}
	if !filepath.IsAbs(game.Target) {
		d.warn("target %q is a relative path; on macOS a quarantined app launched from a relative path can be App-Translocated to a random read-only location (breaking sibling-file assumptions) — prefer an absolute path", game.Target)
	}
	// Resolve to the effective executable (inner .app binary, PATH lookup).
	eff := launch.EffectiveDirectPathTarget(game.Target)
	if resolved, err := exec.LookPath(eff); err == nil {
		eff = resolved
	}
	// Extended attributes are path-specific: a quarantined .app can carry
	// com.apple.quarantine on the bundle root while its inner Contents/MacOS
	// binary does not. Check the configured target as well as the resolved inner
	// executable so the bundle-root case is not missed (design/20).
	checked := ""
	for _, p := range []string{game.Target, eff} {
		if p == "" || p == checked {
			continue
		}
		checked = p
		if targetHasQuarantineAttr(p) {
			d.warn("target %q carries com.apple.quarantine; macOS Gatekeeper may block or translocate it — clear it with `xattr -dr com.apple.quarantine <path>` once you trust the source", p)
			break
		}
	}
}

// targetHasQuarantineAttr reports whether the file has the com.apple.quarantine
// extended attribute (macOS). Uses the base-system `xattr` tool so no build
// tags or extra dependencies are needed; only ever called on darwin.
func targetHasQuarantineAttr(path string) bool {
	out, err := exec.Command("xattr", "-p", "com.apple.quarantine", path).CombinedOutput()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// doctorProfilesAndHooks resolves every launchable context (the default plus
// each named profile), reporting resolvability, resolved hook commands and
// working directories, and the conflation lint on each status hook.
func doctorProfilesAndHooks(d *doctorReport, snap *config.Snapshot, game *config.GameConfig) {
	contexts := []string{""}
	for name := range game.Profiles {
		contexts = append(contexts, name)
	}
	sort.Strings(contexts) // "" sorts first: the default context

	for _, profile := range contexts {
		label := "default context"
		if profile != "" {
			label = fmt.Sprintf("profile %q", profile)
		}
		resolved, rerr := launch.Resolve(snap, launch.Request{GameID: game.ID, Profile: profile}, launch.Options{
			InheritedEnv:       os.Environ(),
			CaseInsensitiveEnv: runtime.GOOS == "windows",
		})
		if rerr != nil {
			d.fail("%s: does not resolve (%s: %s)", label, rerr.Code, rerr.Message)
			continue
		}
		// Report resolvability as a finding, but keep going: the resolved hooks
		// and the conflation advisory are useful even when a target/hook path is
		// unresolvable (that is exactly when a human is inspecting).
		if issues := launch.CheckResolvability(game, resolved); len(issues) > 0 {
			d.fail("%s: unresolvable", label)
			for _, is := range issues {
				d.info("    %s", is.String())
			}
		} else {
			d.info("%s: resolves (cwd %s)", label, displayDir(resolved.WorkingDir))
		}
		if resolved.Lifecycle != nil {
			doctorHook(d, "status", resolved.Lifecycle.Status)
			doctorHook(d, "stop", resolved.Lifecycle.Stop)
			doctorHook(d, "kill", resolved.Lifecycle.Kill)
			if w := conflationWarning(resolved.Lifecycle.Status); w != "" {
				d.warn("%s status hook: %s", label, w)
			}
		}
	}
}

func doctorHook(d *doctorReport, kind string, hook *launch.ResolvedHook) {
	if hook == nil {
		return
	}
	line := fmt.Sprintf("  %s hook: %s", kind, hook.Command)
	if len(hook.Args) > 0 {
		line += " " + strings.Join(hook.Args, " ")
	}
	if hook.WorkingDir != "" {
		line += fmt.Sprintf(" (cwd %s)", hook.WorkingDir)
	}
	d.info("%s", line)
	if hook.Command != "" {
		if _, err := os.Stat(hook.Command); err != nil && filepath.IsAbs(hook.Command) {
			d.warn("%s hook command not found on disk: %s", kind, hook.Command)
		}
	}
}

// conflationWarning returns an advisory when a status hook invokes a tool whose
// exit codes conflate stopped with cannot-determine (design/01/20).
func conflationWarning(hook *launch.ResolvedHook) string {
	if hook == nil || hook.Command == "" {
		return ""
	}
	base := strings.ToLower(filepath.Base(hook.Command))
	if knownConflatingStatusTools[base] {
		return fmt.Sprintf("%q conflates 'stopped' with 'cannot determine' — a raw `%s inspect` exits non-zero both when the target is absent and when its daemon is unreachable, so a daemon hiccup reads as stopped and can unblock a duplicate start. Wrap it to exit an unclassified code when it cannot tell (design/01).", base, base)
	}
	return ""
}

// doctorPermissions warns on broadly readable config/runtime files (design/07;
// the per-launch token must not be world/group readable). Unix only: Windows
// has no POSIX mode bits — the token is protected by NTFS ACLs on the private
// ~/.gabs directory.
func doctorPermissions(d *doctorReport, configDir, gameID string) {
	if runtime.GOOS == "windows" {
		return
	}
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return
	}
	warnLoose := func(path, what string) {
		fi, statErr := os.Stat(path)
		if statErr != nil {
			return
		}
		if fi.Mode().Perm()&0o077 != 0 {
			d.warn("%s is broadly readable (%v): %s — it may carry the per-launch bridge token; tighten to 0600", what, fi.Mode().Perm(), path)
		}
	}
	warnLoose(cp.GetMainConfigPath(), "config file")
	gameDir := cp.GetGameDir(gameID)
	if entries, derr := os.ReadDir(gameDir); derr == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			warnLoose(filepath.Join(gameDir, e.Name()), "runtime file")
		}
	}
}

// doctorTrackRecord prints the full per-profile track record (design/08),
// unconditionally — it is readable from history.json regardless of config
// state.
func doctorTrackRecord(configDir, gameID string) {
	h, err := process.LoadHistory(gameID, configDir)
	if err != nil {
		fmt.Printf("Track record: unreadable (%v)\n", err)
		return
	}
	if h == nil || len(h.Profiles) == 0 {
		fmt.Println("Track record: none recorded")
		return
	}
	fmt.Println("Track record:")
	for _, profile := range sortedProfiles(h.Profiles) {
		e := h.Profiles[profile]
		fmt.Printf("  %s: %s\n", profileLabel(profile), process.TrackRecordLine(e))
		if e.LastFailure != nil {
			names := ""
			if len(e.LastFailure.InputNames) > 0 {
				names = " [inputs: " + strings.Join(e.LastFailure.InputNames, ", ") + "]"
			}
			fmt.Printf("      last failure: %s / %s, %s ago%s\n",
				e.LastFailure.Outcome, causeClassLabel(e.LastFailure.Class), humanizeSince(e.LastFailure.At), names)
		}
	}
}

// doctorLastGood prints the last-known-good entry per profile (design/08), so a
// human can compare or restore an edited context by hand. GABS never restores
// automatically.
func doctorLastGood(log util.Logger, configDir, gameID string, snap *config.Snapshot, game *config.GameConfig) {
	h, err := process.LoadHistory(gameID, configDir)
	if err != nil || h == nil || len(h.LastGood) == 0 {
		fmt.Println("Last known good: none recorded")
		return
	}
	// Compute the current resolved context hash per profile to flag edits.
	currentHash := map[string]string{}
	if snap != nil && game != nil {
		m := cliClaimManager(log, configDir)
		for profile := range h.LastGood {
			if resolved, rerr := launch.Resolve(snap, launch.Request{GameID: game.ID, Profile: profile}, launch.Options{
				InheritedEnv:       os.Environ(),
				CaseInsensitiveEnv: runtime.GOOS == "windows",
			}); rerr == nil {
				currentHash[profile] = m.ComputeHistoryContext(snap, *game, resolved, nil).ContextHash
			}
		}
	}
	fmt.Println("Last known good:")
	for _, profile := range sortedLastGood(h.LastGood) {
		lg := h.LastGood[profile]
		fmt.Printf("  %s (recorded %s ago):\n", profileLabel(profile), humanizeSince(lg.At))
		fmt.Printf("      context hash: %s\n", lg.ContextHash)
		fmt.Printf("      target: %s  mode: %s\n", lg.EntrySnapshot.Target, lg.EntrySnapshot.Mode)
		if len(lg.EntrySnapshot.Args) > 0 {
			fmt.Printf("      args: %s\n", strings.Join(lg.EntrySnapshot.Args, " "))
		}
		if lg.EntrySnapshot.WorkingDir != "" {
			fmt.Printf("      workingDir: %s\n", lg.EntrySnapshot.WorkingDir)
		}
		if cur, ok := currentHash[profile]; ok && cur != lg.ContextHash {
			fmt.Println("      NOTE: the current context differs from this proven one — it was edited. Compare or restore by hand; GABS never restores automatically.")
		}
	}
}

// --- small display helpers ---

func displayDir(dir string) string {
	if dir == "" {
		return "(inherited)"
	}
	return dir
}

func profileLabel(profile string) string {
	if profile == "" {
		return `profile "" (default)`
	}
	return fmt.Sprintf("profile %q", profile)
}

func causeClassLabel(class string) string {
	if class == "" {
		return "unclassified"
	}
	return class
}

func sortedProfiles(m map[string]*process.HistoryEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedLastGood(m map[string]*process.HistoryLastGood) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func humanizeSince(t time.Time) string {
	if t.IsZero() {
		return "unknown time"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
