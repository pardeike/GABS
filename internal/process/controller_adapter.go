package process

import "time"

// ControllerInterface defines the interface for process controllers
type ControllerInterface interface {
	Configure(spec LaunchSpec) error
	SetBridgeInfo(port int, token string)
	Start() error
	Stop(grace time.Duration) error
	Kill() error
	IsRunning() bool
	GetPID() int
	GetLaunchMode() string
	GetStopProcessName() string
	IsLauncherProcessRunning() bool
	FinalEnvironment() []string
	LaunchLogTail(maxBytes int64) string
	SetSpawnObservers(before func(), after func(pid int, startTime int64, spawnErr error))
	DirectChildExited() bool
	ExitCode() int
}

// NewController creates a new controller instance
// This maintains the existing API while using the consolidated implementation
func NewController() ControllerInterface {
	return &Controller{}
}

// Ensure Controller implements ControllerInterface
var _ ControllerInterface = (*Controller)(nil)
