package lifecycle

import (
	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
)

// LaunchSpecFromGame builds the base process spec from a game's static config.
func LaunchSpecFromGame(game config.GameConfig) process.LaunchSpec {
	return process.LaunchSpec{
		GameId:          game.ID,
		Mode:            game.LaunchMode,
		PathOrId:        game.Target,
		Args:            game.Args,
		WorkingDir:      game.WorkingDir,
		StopProcessName: game.StopProcessName,
	}
}

// LaunchSpecFromResolved builds the process spec from the resolver output:
// resolved args/env/cwd plus profile context, with macOS .app bundle targets
// resolved to their inner executable for propagation-capable modes.
func LaunchSpecFromResolved(game config.GameConfig, r *launch.Resolved) process.LaunchSpec {
	spec := LaunchSpecFromGame(game)
	// Bundle resolution applies to every propagation-capable path mode:
	// Stage 1 checks the inner executable, so the spawn must exec the same
	// effective target or a passing check would still spawn_fail.
	if game.LaunchMode == "DirectPath" || game.LaunchMode == "" || game.LaunchMode == "CustomCommand" {
		spec.PathOrId = launch.EffectiveDirectPathTarget(game.Target)
	}
	if r == nil {
		return spec
	}
	spec.Args = append([]string(nil), r.Args...)
	spec.WorkingDir = r.WorkingDir
	spec.Profile = r.Profile
	spec.Env = r.Env
	spec.ContextEnvKeys = append([]string(nil), r.ContextEnvKeys...)
	spec.AbsentEnvNames = append([]string(nil), r.AbsentEnvNames...)
	spec.AppliedInputs = append([]string(nil), r.AppliedInputs...)
	spec.ConfigRevision = r.ConfigRevision
	spec.Lifecycle = r.Lifecycle
	return spec
}

// LaunchSpecWithRuntimeDir stamps the per-game runtime directory onto a spec so
// the spawned workload's bridge files land under this manager's config dir.
func (m *Manager) LaunchSpecWithRuntimeDir(spec process.LaunchSpec) process.LaunchSpec {
	if cp, err := config.NewConfigPaths(m.configDir); err == nil {
		spec.RuntimeDir = cp.GetGameDir(spec.GameId)
	}
	return spec
}
