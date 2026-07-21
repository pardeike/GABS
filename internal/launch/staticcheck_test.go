package launch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/config"
)

func writeExec(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolvabilityDirectPath(t *testing.T) {
	dir := t.TempDir()
	target := writeExec(t, dir, "game")

	g := &config.GameConfig{ID: "g", LaunchMode: "DirectPath", Target: target}
	if issues := CheckResolvability(g, &Resolved{}); len(issues) != 0 {
		t.Fatalf("existing executable must resolve: %v", issues)
	}

	g.Target = filepath.Join(dir, "missing")
	issues := CheckResolvability(g, &Resolved{})
	if len(issues) != 1 || issues[0].JSONPath != "/games/g/target" {
		t.Fatalf("missing target must fail with JSON path: %v", issues)
	}

	// empty target is unresolvable, not silently skipped
	g.Target = ""
	if issues := CheckResolvability(g, &Resolved{}); len(issues) != 1 || !strings.Contains(issues[0].Message, "empty") {
		t.Fatalf("empty target must be unresolvable: %v", issues)
	}
}

func TestResolvabilityRelativeToWorkingDir(t *testing.T) {
	// legacy-valid: target "./run.sh" with workingDir /opt/game — exec.Cmd
	// resolves the target after chdir, and the static check must match.
	dir := t.TempDir()
	writeExec(t, dir, "run.sh")

	g := &config.GameConfig{ID: "g", LaunchMode: "DirectPath", Target: "./run.sh"}
	r := &Resolved{WorkingDir: dir}
	if issues := CheckResolvability(g, r); len(issues) != 0 {
		t.Fatalf("relative target must resolve against the effective workingDir: %v", issues)
	}
	// and fail against a different cwd
	r2 := &Resolved{WorkingDir: t.TempDir()}
	if issues := CheckResolvability(g, r2); len(issues) == 0 {
		t.Fatalf("relative target must fail when workingDir lacks it")
	}
}

func TestResolvabilityCustomCommand(t *testing.T) {
	dir := t.TempDir()
	target := writeExec(t, dir, "server")

	g := &config.GameConfig{ID: "g", LaunchMode: "CustomCommand", Target: target}
	if issues := CheckResolvability(g, &Resolved{}); len(issues) != 0 {
		t.Fatalf("CustomCommand executable must resolve: %v", issues)
	}

	// a multi-word target cannot exec; the hint explains why
	g.Target = target + " -nographics -batchmode"
	issues := CheckResolvability(g, &Resolved{})
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "arguments") {
		t.Fatalf("multi-word CustomCommand target must fail with the args hint: %v", issues)
	}
}

func TestResolvabilitySteamManaged(t *testing.T) {
	orig := steamResolveExecutable
	defer func() { steamResolveExecutable = orig }()

	dir := t.TempDir()
	exe := writeExec(t, dir, "steamgame")

	steamResolveExecutable = func(appID string) (string, error) { return exe, nil }
	g := &config.GameConfig{ID: "g", LaunchMode: "SteamManaged", Target: "123456"}
	if issues := CheckResolvability(g, &Resolved{}); len(issues) != 0 {
		t.Fatalf("resolvable Steam app must pass: %v", issues)
	}

	steamResolveExecutable = func(appID string) (string, error) { return "", errors.New("app not installed") }
	if issues := CheckResolvability(g, &Resolved{}); len(issues) != 1 || !strings.Contains(issues[0].Message, "not installed") {
		t.Fatalf("unresolvable Steam app must fail with the resolver error: %v", issues)
	}

	steamResolveExecutable = func(appID string) (string, error) { return filepath.Join(dir, "gone"), nil }
	if issues := CheckResolvability(g, &Resolved{}); len(issues) != 1 {
		t.Fatalf("missing resolved executable must fail: %v", issues)
	}
}

func TestHookCommandPathPinning(t *testing.T) {
	dir := t.TempDir()
	writeExec(t, dir, "checker")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	g := config.GameConfig{ID: "g", Name: "G", LaunchMode: "DirectPath", Target: "/x",
		Lifecycle: &config.LifecycleConfig{Status: &config.HookConfig{Command: "checker"}}}
	snap := snapWith(g)
	r, rerr := Resolve(snap, Request{GameID: "g"}, Options{})
	if rerr != nil {
		t.Fatal(rerr)
	}
	want := filepath.Join(dir, "checker")
	if r.Lifecycle.Status.Command != want {
		t.Fatalf("hook command must pin to absolute path: got %q want %q", r.Lifecycle.Status.Command, want)
	}
	// unresolvable commands keep the literal (static checks surface it)
	g.Lifecycle.Status.Command = "no-such-hook-cmd"
	r, _ = Resolve(snapWith(g), Request{GameID: "g"}, Options{})
	if r.Lifecycle.Status.Command != "no-such-hook-cmd" {
		t.Fatalf("unresolvable command must keep literal, got %q", r.Lifecycle.Status.Command)
	}
}

func TestCheckResolvabilityHooks(t *testing.T) {
	dir := t.TempDir()
	target := writeExec(t, dir, "game-bin")
	hookExe := writeExec(t, dir, "status-hook")

	game := config.GameConfig{ID: "g", Name: "G", LaunchMode: "DirectPath", Target: target}

	// bare command absent from PATH: the resolution-time pin failed and
	// Stage 1 must say so, not persist an unusable hook
	r := &Resolved{Lifecycle: &ResolvedLifecycle{
		Status: &ResolvedHook{Command: "definitely-not-on-path-xyz"},
	}}
	issues := CheckResolvability(&game, r)
	if len(issues) != 1 || issues[0].JSONPath != "/games/g/lifecycle/status/command" {
		t.Fatalf("missing PATH hook must fail Stage 1 with its JSON path: %v", issues)
	}

	// path-containing missing command
	r = &Resolved{Lifecycle: &ResolvedLifecycle{
		Stop: &ResolvedHook{Command: filepath.Join(dir, "missing-hook")},
	}}
	issues = CheckResolvability(&game, r)
	if len(issues) != 1 || issues[0].JSONPath != "/games/g/lifecycle/stop/command" ||
		issues[0].FSPath != filepath.Join(dir, "missing-hook") {
		t.Fatalf("missing hook path must fail with fs path: %v", issues)
	}

	// resolvable hook passes
	r = &Resolved{Lifecycle: &ResolvedLifecycle{
		Status: &ResolvedHook{Command: hookExe},
	}}
	if issues = CheckResolvability(&game, r); len(issues) != 0 {
		t.Fatalf("existing executable hook must pass: %v", issues)
	}

	// profile override reports the profile's JSON path
	game.Profiles = map[string]config.ProfileConfig{
		"p": {Description: "d", Lifecycle: &config.LifecycleConfig{
			Kill: &config.HookConfig{Command: "x"},
		}},
	}
	r = &Resolved{Profile: "p", Lifecycle: &ResolvedLifecycle{
		Kill: &ResolvedHook{Command: filepath.Join(dir, "gone")},
	}}
	issues = CheckResolvability(&game, r)
	if len(issues) != 1 || issues[0].JSONPath != "/games/g/profiles/p/lifecycle/kill/command" {
		t.Fatalf("profile-override hook must report the profile path: %v", issues)
	}
}
