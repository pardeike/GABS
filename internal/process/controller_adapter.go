package process

import "time"

// ControllerInterface defines the interface that both old and new controllers implement
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
}

// ControllerAdapter wraps the ImprovedController to maintain compatibility with existing code
type ControllerAdapter struct {
	improved *ImprovedController
}

// NewController creates a new controller using the improved implementation
func NewController() ControllerInterface {
	return &ControllerAdapter{
		improved: NewImprovedController(),
	}
}

// Configure implements ControllerInterface
func (a *ControllerAdapter) Configure(spec LaunchSpec) error {
	return a.improved.Configure(spec)
}

// SetBridgeInfo implements ControllerInterface
func (a *ControllerAdapter) SetBridgeInfo(port int, token string) {
	a.improved.SetBridgeInfo(port, token)
}

// Start implements ControllerInterface
func (a *ControllerAdapter) Start() error {
	return a.improved.Start()
}

// Stop implements ControllerInterface
func (a *ControllerAdapter) Stop(grace time.Duration) error {
	return a.improved.Stop(grace)
}

// Kill implements ControllerInterface
func (a *ControllerAdapter) Kill() error {
	return a.improved.Kill()
}

// IsRunning implements ControllerInterface
func (a *ControllerAdapter) IsRunning() bool {
	return a.improved.IsRunning()
}

// GetPID implements ControllerInterface
func (a *ControllerAdapter) GetPID() int {
	return a.improved.GetPID()
}

// GetLaunchMode implements ControllerInterface
func (a *ControllerAdapter) GetLaunchMode() string {
	return a.improved.GetLaunchMode()
}

// GetStopProcessName implements ControllerInterface
func (a *ControllerAdapter) GetStopProcessName() string {
	return a.improved.GetStopProcessName()
}

// IsLauncherProcessRunning implements ControllerInterface
func (a *ControllerAdapter) IsLauncherProcessRunning() bool {
	return a.improved.IsLauncherProcessRunning()
}

// GetState returns the current process state (additional method for improved functionality)
func (a *ControllerAdapter) GetState() ProcessState {
	return a.improved.GetState()
}

// GetLastError returns the last error (additional method for improved functionality)
func (a *ControllerAdapter) GetLastError() error {
	return a.improved.state.GetLastError()
}

// Cleanup releases resources (additional method for improved functionality)
func (a *ControllerAdapter) Cleanup() {
	a.improved.Cleanup()
}