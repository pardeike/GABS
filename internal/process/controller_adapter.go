package process

import "time"

// ControllerAdapter wraps the RefactoredController to maintain compatibility with existing code
type ControllerAdapter struct {
	refactored *RefactoredController
}

// NewController creates a new controller using the refactored implementation
func NewController() ControllerInterface {
	return &ControllerAdapter{
		refactored: NewRefactoredController(),
	}
}

// Configure implements ControllerInterface
func (a *ControllerAdapter) Configure(spec LaunchSpec) error {
	return a.refactored.Configure(spec)
}

// SetBridgeInfo implements ControllerInterface
func (a *ControllerAdapter) SetBridgeInfo(port int, token string) {
	a.refactored.SetBridgeInfo(port, token)
}

// Start implements ControllerInterface
func (a *ControllerAdapter) Start() error {
	return a.refactored.Start()
}

// Stop implements ControllerInterface
func (a *ControllerAdapter) Stop(grace time.Duration) error {
	return a.refactored.Stop(grace)
}

// Kill implements ControllerInterface
func (a *ControllerAdapter) Kill() error {
	return a.refactored.Kill()
}

// IsRunning implements ControllerInterface
func (a *ControllerAdapter) IsRunning() bool {
	return a.refactored.IsRunning()
}

// GetPID implements ControllerInterface
func (a *ControllerAdapter) GetPID() int {
	return a.refactored.GetPID()
}

// GetLaunchMode implements ControllerInterface
func (a *ControllerAdapter) GetLaunchMode() string {
	return a.refactored.GetLaunchMode()
}

// GetStopProcessName implements ControllerInterface
func (a *ControllerAdapter) GetStopProcessName() string {
	return a.refactored.GetStopProcessName()
}

// IsLauncherProcessRunning implements ControllerInterface
func (a *ControllerAdapter) IsLauncherProcessRunning() bool {
	return a.refactored.IsLauncherProcessRunning()
}

// WaitForProcessStart exposes the verification capability (additional method)
func (a *ControllerAdapter) WaitForProcessStart(timeout time.Duration) error {
	return a.refactored.WaitForProcessStart(timeout)
}