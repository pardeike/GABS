package mcp

import (
	"time"

	"github.com/pardeike/gabs/internal/process"
)

// deadFakePID is a PID that VerifyPIDFingerprint reports StatusStopped for
// (not found), with a nonzero recorded start-time so it is never treated as a
// legacy "alive" claim. A fake controller stamps it as the spawned PID, so the
// Stage-4/Stage-5 liveness assessment is DETERMINISTIC — the workload is
// provably gone — regardless of subprocess timing under -race (round 12 F5).
const deadFakePID = 2147483646

// fakeController implements process.ControllerInterface with controllable,
// deterministic liveness. IsRunning() drives the Stage-4 promote decision
// (controllerLooksAlive); the stamped dead PID drives the assessWorkload
// liveness verdict. running=false → exit before Stage 4 (no workloadStart);
// running=true → verified at Stage 4 (workloadStart) then Stage-5 death.
type fakeController struct {
	running        bool
	aliveThenDead  bool // report running on the first IsRunning check, dead after
	isRunningCalls int
	exitCode       int
	mode           string
	afterObs       func(pid int, startTime int64, spawnErr error)
	beforeObs      func() error
}

func newExitBeforeStage4Controller() process.ControllerInterface {
	return &fakeController{running: false, exitCode: 1, mode: "DirectPath"}
}

// newExitBeforeStage4ControllerMode is the mode-parameterized variant used to
// prove exited_during_start is game-class regardless of launch mode (F6).
func newExitBeforeStage4ControllerMode(mode string) func() process.ControllerInterface {
	return func() process.ControllerInterface {
		return &fakeController{running: false, exitCode: 1, mode: mode}
	}
}

func newVerifiedThenDeathController() process.ControllerInterface {
	// running for the Stage-4 verification, dead for the Stage-5 bridge wait.
	return &fakeController{aliveThenDead: true, exitCode: 1, mode: "DirectPath"}
}

func (f *fakeController) Configure(spec process.LaunchSpec) error { return nil }
func (f *fakeController) SetBridgeInfo(port int, token string)    {}
func (f *fakeController) Start() error {
	if f.beforeObs != nil {
		if err := f.beforeObs(); err != nil {
			return err
		}
	}
	// Report a successful spawn of a PID that is already gone: the claim's
	// GamePID fingerprint verifies StatusStopped deterministically.
	if f.afterObs != nil {
		f.afterObs(deadFakePID, 1, nil)
	}
	return nil
}
func (f *fakeController) Stop(grace time.Duration) error { return nil }
func (f *fakeController) Kill() error                    { return nil }
func (f *fakeController) IsRunning() bool {
	f.isRunningCalls++
	if f.aliveThenDead {
		// Alive for the single Stage-4 verification, dead for every Stage-5
		// bridge-wait check — a deterministic verified-then-death sequence.
		return f.isRunningCalls <= 1
	}
	return f.running
}
func (f *fakeController) GetPID() int { return deadFakePID }
func (f *fakeController) GetLaunchMode() string {
	if f.mode != "" {
		return f.mode
	}
	return "DirectPath"
}
func (f *fakeController) GetStopProcessName() string     { return "" }
func (f *fakeController) IsLauncherProcessRunning() bool { return false }
func (f *fakeController) FinalEnvironment() []string     { return nil }
func (f *fakeController) LaunchLogTail(maxBytes int64) string {
	return "fake controller: workload exited during start"
}
func (f *fakeController) SetSpawnObservers(before func() error, after func(pid int, startTime int64, spawnErr error)) {
	f.beforeObs = before
	f.afterObs = after
}
func (f *fakeController) DirectChildExited() bool                       { return !f.running }
func (f *fakeController) ExitCode() int                                 { return f.exitCode }
func (f *fakeController) TerminateDirectChild()                         {}
func (f *fakeController) MaterializeSpawnSpec() (string, string, error) { return "/fake/exe", "", nil }
