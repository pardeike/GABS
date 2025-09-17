package process

import (
	"fmt"
	"sync"
	"time"
)

// ProcessState represents the current state of a game process
type ProcessState int

const (
	ProcessStateStopped ProcessState = iota
	ProcessStateStarting
	ProcessStateRunning
	ProcessStateStopping
	ProcessStateUnknown // For launcher-based games without tracking
)

func (s ProcessState) String() string {
	switch s {
	case ProcessStateStopped:
		return "stopped"
	case ProcessStateStarting:
		return "starting"
	case ProcessStateRunning:
		return "running"
	case ProcessStateStopping:
		return "stopping"
	case ProcessStateUnknown:
		return "unknown"
	default:
		return "invalid"
	}
}

// ProcessError represents different types of process-related errors
type ProcessError struct {
	Type    ProcessErrorType
	Context string
	Err     error
}

type ProcessErrorType int

const (
	ProcessErrorTypeConfiguration ProcessErrorType = iota
	ProcessErrorTypeStart
	ProcessErrorTypeStop
	ProcessErrorTypeStatus
	ProcessErrorTypeNotFound
)

func (e *ProcessError) Error() string {
	switch e.Type {
	case ProcessErrorTypeConfiguration:
		return fmt.Sprintf("configuration error (%s): %v", e.Context, e.Err)
	case ProcessErrorTypeStart:
		return fmt.Sprintf("start error (%s): %v", e.Context, e.Err)
	case ProcessErrorTypeStop:
		return fmt.Sprintf("stop error (%s): %v", e.Context, e.Err)
	case ProcessErrorTypeStatus:
		return fmt.Sprintf("status check error (%s): %v", e.Context, e.Err)
	case ProcessErrorTypeNotFound:
		return fmt.Sprintf("process not found (%s): %v", e.Context, e.Err)
	default:
		return fmt.Sprintf("process error (%s): %v", e.Context, e.Err)
	}
}

// ProcessStateTracker manages the state of a process with proper synchronization
type ProcessStateTracker struct {
	mu           sync.RWMutex
	state        ProcessState
	lastError    error
	pid          int
	startTime    time.Time
	stopTime     time.Time
	launchMode   string
	gameId       string
}

// NewProcessStateTracker creates a new process state tracker
func NewProcessStateTracker(gameId, launchMode string) *ProcessStateTracker {
	return &ProcessStateTracker{
		state:      ProcessStateStopped,
		launchMode: launchMode,
		gameId:     gameId,
	}
}

// GetState returns the current process state (thread-safe)
func (t *ProcessStateTracker) GetState() ProcessState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state
}

// SetState updates the process state (thread-safe)
func (t *ProcessStateTracker) SetState(state ProcessState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	prevState := t.state
	t.state = state
	
	// Track timing
	now := time.Now()
	switch state {
	case ProcessStateRunning:
		if prevState == ProcessStateStarting {
			t.startTime = now
		}
	case ProcessStateStopped:
		if prevState != ProcessStateStopped {
			t.stopTime = now
		}
	}
}

// SetError records an error and updates state appropriately
func (t *ProcessStateTracker) SetError(err error, errType ProcessErrorType, context string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	t.lastError = &ProcessError{
		Type:    errType,
		Context: context,
		Err:     err,
	}
	
	// Update state based on error type
	switch errType {
	case ProcessErrorTypeStart:
		t.state = ProcessStateStopped
	case ProcessErrorTypeStop:
		// Keep current state, error doesn't necessarily mean process is stopped
	case ProcessErrorTypeStatus:
		// Status errors might indicate unknown state
		if t.launchMode == "SteamAppId" || t.launchMode == "EpicAppId" {
			t.state = ProcessStateUnknown
		}
	}
}

// GetLastError returns the last error (thread-safe)
func (t *ProcessStateTracker) GetLastError() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastError
}

// SetPID updates the process ID
func (t *ProcessStateTracker) SetPID(pid int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pid = pid
}

// GetPID returns the process ID
func (t *ProcessStateTracker) GetPID() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.pid
}

// GetStats returns process statistics
func (t *ProcessStateTracker) GetStats() (startTime, stopTime time.Time, uptime time.Duration) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	
	startTime = t.startTime
	stopTime = t.stopTime
	
	if !t.startTime.IsZero() {
		if t.state == ProcessStateRunning {
			uptime = time.Since(t.startTime)
		} else if !t.stopTime.IsZero() {
			uptime = t.stopTime.Sub(t.startTime)
		}
	}
	
	return
}

// IsRunning returns true if the process is currently running
func (t *ProcessStateTracker) IsRunning() bool {
	state := t.GetState()
	return state == ProcessStateRunning || state == ProcessStateStarting
}

// IsHealthy returns true if the process is in a good state (running without recent errors)
func (t *ProcessStateTracker) IsHealthy() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	
	return t.state == ProcessStateRunning && t.lastError == nil
}