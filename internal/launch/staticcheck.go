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
)

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
// resolved launch: misconfiguration fails fast with a path instead of a
// spawn-and-die. Covered now: DirectPath targets (including macOS .app
// resolution) and effective working directories. SteamManaged executable
// resolution happens inside the Steam resolver at spawn; CustomCommand
// target semantics are launcher-defined — both remain spawn-time failures
// for this milestone (recorded in PROGRESS).
func CheckResolvability(game *config.GameConfig, r *Resolved) []SpecIssue {
	var issues []SpecIssue
	base := "/games/" + escapeJSONPointer(game.ID)

	if game.LaunchMode == "DirectPath" || game.LaunchMode == "" {
		target := EffectiveDirectPathTarget(game.Target)
		if target != "" {
			if strings.ContainsRune(target, os.PathSeparator) || strings.ContainsRune(target, '/') {
				if fi, err := os.Stat(target); err != nil {
					issues = append(issues, SpecIssue{
						Code: "launch_spec_unresolvable", JSONPath: base + "/target", FSPath: target,
						Message: "target does not exist"})
				} else if fi.IsDir() {
					issues = append(issues, SpecIssue{
						Code: "launch_spec_unresolvable", JSONPath: base + "/target", FSPath: target,
						Message: "target is a directory, not an executable"})
				} else if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
					issues = append(issues, SpecIssue{
						Code: "launch_spec_unresolvable", JSONPath: base + "/target", FSPath: target,
						Message: "target is not executable"})
				}
			} else if _, err := exec.LookPath(target); err != nil {
				issues = append(issues, SpecIssue{
					Code: "launch_spec_unresolvable", JSONPath: base + "/target", FSPath: target,
					Message: "target is not resolvable via PATH"})
			}
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
	return issues
}

func escapeJSONPointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

var bundleExecutableRe = regexp.MustCompile(`<key>\s*CFBundleExecutable\s*</key>\s*<string>([^<]+)</string>`)

// EffectiveDirectPathTarget resolves a macOS .app bundle directory to its
// inner executable (Contents/MacOS/<CFBundleExecutable>). LaunchServices
// (`open`) is never used for propagation-capable modes — it drops argv and
// env silently (design/03-context-delivery.md, platform rules). Non-bundle
// targets and other platforms pass through unchanged.
func EffectiveDirectPathTarget(target string) string {
	if runtime.GOOS != "darwin" || !strings.HasSuffix(target, ".app") {
		return target
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		return target
	}
	execName := strings.TrimSuffix(filepath.Base(target), ".app")
	// Info.plist is usually XML in developer bundles; binary plists fall
	// back to the bundle-name convention rather than pulling a dependency.
	if data, err := os.ReadFile(filepath.Join(target, "Contents", "Info.plist")); err == nil {
		if m := bundleExecutableRe.FindSubmatch(data); m != nil {
			execName = strings.TrimSpace(string(m[1]))
		}
	}
	inner := filepath.Join(target, "Contents", "MacOS", execName)
	if _, err := os.Stat(inner); err == nil {
		return inner
	}
	return target
}
