package launch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/steam"
)

// steamResolveExecutable is injectable for tests (reads Steam library
// manifests; no side effects).
var steamResolveExecutable = func(appID string) (string, error) {
	app, err := steam.ResolveApp(appID)
	if err != nil {
		return "", err
	}
	return app.Executable, nil
}

// SetSteamResolveExecutableForTesting swaps the SteamManaged resolvability
// probe so a test can pin a resolved executable without a real Steam install
// (round 12 F6 production tests). Returns a restore func.
func SetSteamResolveExecutableForTesting(fn func(appID string) (string, error)) func() {
	prev := steamResolveExecutable
	steamResolveExecutable = fn
	return func() { steamResolveExecutable = prev }
}

// SpecIssue is one Stage 1 static-resolvability failure: the config points
// at something that does not resolve on this machine. Maps to the stable
// code launch_spec_unresolvable with both the JSON path and the resolved
// filesystem path (design/05-start-pipeline.md).
type SpecIssue struct {
	Code     string // always "launch_spec_unresolvable"
	JSONPath string
	FSPath   string
	Message  string
}

func (i SpecIssue) String() string {
	return fmt.Sprintf("%s (%s -> %s)", i.Message, i.JSONPath, i.FSPath)
}

// CheckResolvability performs the Stage 1 static filesystem checks for a
// resolved launch: misconfiguration fails fast, side-effect-free, with a
// path instead of a spawn-and-die. All three propagation-capable modes are
// covered: DirectPath and CustomCommand targets (executed as a single
// executable path, exactly like the process controller execs them) and
// SteamManaged via the read-only Steam library resolver. URL modes have
// opaque targets and are not path-checked.
func CheckResolvability(game *config.GameConfig, r *Resolved) []SpecIssue {
	var issues []SpecIssue
	base := "/games/" + escapeJSONPointer(game.ID)
	targetPath := base + "/target"

	add := func(fsPath, msg string) {
		issues = append(issues, SpecIssue{
			Code: "launch_spec_unresolvable", JSONPath: targetPath, FSPath: fsPath, Message: msg})
	}

	switch game.LaunchMode {
	case "DirectPath", "", "CustomCommand":
		target := EffectiveBundleTarget(game.Target)
		if strings.TrimSpace(target) == "" {
			add("", "target is empty; set it before starting")
			break
		}
		if strings.ContainsRune(target, os.PathSeparator) || strings.ContainsRune(target, '/') {
			// exec.Cmd resolves a relative path-containing command after
			// changing to the configured directory — check it the same way,
			// or legacy-valid "./run.sh" + workingDir configs would fail.
			checkPath := target
			if !filepath.IsAbs(checkPath) && r.WorkingDir != "" {
				checkPath = filepath.Join(r.WorkingDir, checkPath)
			}
			if fi, err := os.Stat(checkPath); err != nil {
				msg := "target does not exist"
				if game.LaunchMode == "CustomCommand" && strings.ContainsRune(target, ' ') {
					msg += " (CustomCommand targets are executed as a single executable path; put arguments in args)"
				}
				add(checkPath, msg)
			} else if fi.IsDir() {
				add(checkPath, "target is a directory, not an executable")
			} else if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
				add(checkPath, "target is not executable")
			}
		} else if _, err := exec.LookPath(target); err != nil {
			add(target, "target is not resolvable via PATH")
		}
	case "SteamManaged":
		exe, err := steamResolveExecutable(game.Target)
		if err != nil {
			add(game.Target, fmt.Sprintf("Steam app is not resolvable: %v", err))
		} else if _, statErr := os.Stat(exe); statErr != nil {
			add(exe, "resolved Steam executable does not exist")
		}
	}

	if r.WorkingDir != "" {
		jsonPath := base + "/workingDir"
		if r.Profile != "" {
			if p, ok := game.Profiles[r.Profile]; ok && p.WorkingDir != "" {
				jsonPath = base + "/profiles/" + escapeJSONPointer(r.Profile) + "/workingDir"
			}
		}
		if fi, err := os.Stat(r.WorkingDir); err != nil {
			issues = append(issues, SpecIssue{
				Code: "launch_spec_unresolvable", JSONPath: jsonPath, FSPath: r.WorkingDir,
				Message: "working directory does not exist"})
		} else if !fi.IsDir() {
			issues = append(issues, SpecIssue{
				Code: "launch_spec_unresolvable", JSONPath: jsonPath, FSPath: r.WorkingDir,
				Message: "working directory is not a directory"})
		}
	}

	// Lifecycle hooks fail Stage 1 too: persisting an unusable hook into
	// the claim would surface later as unknown/action failures instead of
	// the precise launch_spec_unresolvable with a path (design/05 Stage 1).
	if r.Lifecycle != nil {
		for _, hk := range []struct {
			kind string
			hook *ResolvedHook
		}{{"status", r.Lifecycle.Status}, {"stop", r.Lifecycle.Stop}, {"kill", r.Lifecycle.Kill}} {
			if hk.hook == nil {
				continue
			}
			issues = append(issues, checkHookResolvability(game, r, hk.kind, hk.hook)...)
		}
	}
	return issues
}

// checkHookResolvability mirrors how the hook runner executes the command:
// separator-containing paths relative to the hook's working directory,
// bare names via PATH (already pinned to absolute at resolution when found
// — a still-bare command means the pin failed).
func checkHookResolvability(game *config.GameConfig, r *Resolved, kind string, h *ResolvedHook) []SpecIssue {
	base := "/games/" + escapeJSONPointer(game.ID)
	jsonPath := base + "/lifecycle/" + kind + "/command"
	if r.Profile != "" {
		if p, ok := game.Profiles[r.Profile]; ok && p.Lifecycle != nil {
			override := false
			switch kind {
			case "status":
				override = p.Lifecycle.Status != nil
			case "stop":
				override = p.Lifecycle.Stop != nil
			case "kill":
				override = p.Lifecycle.Kill != nil
			}
			if override {
				jsonPath = base + "/profiles/" + escapeJSONPointer(r.Profile) + "/lifecycle/" + kind + "/command"
			}
		}
	}

	cmd := h.Command
	if strings.ContainsRune(cmd, os.PathSeparator) || strings.ContainsRune(cmd, '/') {
		checkPath := cmd
		if !filepath.IsAbs(checkPath) && h.WorkingDir != "" {
			checkPath = filepath.Join(h.WorkingDir, checkPath)
		}
		if fi, err := os.Stat(checkPath); err != nil {
			return []SpecIssue{{Code: "launch_spec_unresolvable", JSONPath: jsonPath, FSPath: checkPath,
				Message: kind + " hook command does not exist"}}
		} else if fi.IsDir() {
			return []SpecIssue{{Code: "launch_spec_unresolvable", JSONPath: jsonPath, FSPath: checkPath,
				Message: kind + " hook command is a directory, not an executable"}}
		} else if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
			return []SpecIssue{{Code: "launch_spec_unresolvable", JSONPath: jsonPath, FSPath: checkPath,
				Message: kind + " hook command is not executable"}}
		}
		return nil
	}
	if _, err := exec.LookPath(cmd); err != nil {
		return []SpecIssue{{Code: "launch_spec_unresolvable", JSONPath: jsonPath, FSPath: cmd,
			Message: kind + " hook command is not resolvable via PATH"}}
	}
	return nil
}

func escapeJSONPointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

var bundleExecutableRe = regexp.MustCompile(`<key>\s*CFBundleExecutable\s*</key>\s*<string>([^<]+)</string>`)

// EffectiveBundleTarget resolves a macOS .app bundle directory to its inner
// executable for the propagation-capable modes — LaunchServices (`open`) is
// never used for them, since it drops argv and env silently (design/03,
// platform rules). Resolution order: XML Info.plist CFBundleExecutable →
// single executable in Contents/MacOS → bundle-name convention. Binary
// plists are covered by the directory scan (parsing bplist without a
// dependency is not worth the risk). Non-bundle targets and other
// platforms pass through unchanged.
func EffectiveBundleTarget(target string) string {
	if runtime.GOOS != "darwin" || !strings.HasSuffix(target, ".app") {
		return target
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		return target
	}
	macOSDir := filepath.Join(target, "Contents", "MacOS")

	// 1) XML Info.plist
	if data, err := os.ReadFile(filepath.Join(target, "Contents", "Info.plist")); err == nil {
		if m := bundleExecutableRe.FindSubmatch(data); m != nil {
			inner := filepath.Join(macOSDir, strings.TrimSpace(string(m[1])))
			if _, err := os.Stat(inner); err == nil {
				return inner
			}
		}
	}
	// 2) Sole executable in Contents/MacOS (covers binary plists)
	if entries, err := os.ReadDir(macOSDir); err == nil {
		var candidates []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if info, err := e.Info(); err == nil && info.Mode()&0o111 != 0 {
				candidates = append(candidates, e.Name())
			}
		}
		if len(candidates) == 1 {
			return filepath.Join(macOSDir, candidates[0])
		}
	}
	// 3) Bundle-name convention
	inner := filepath.Join(macOSDir, strings.TrimSuffix(filepath.Base(target), ".app"))
	if _, err := os.Stat(inner); err == nil {
		return inner
	}
	return target
}

// EffectiveDirectPathTarget is retained for callers that predate
// EffectiveBundleTarget; both propagation-capable path modes resolve
// bundles identically.
func EffectiveDirectPathTarget(target string) string { return EffectiveBundleTarget(target) }
