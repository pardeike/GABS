package process

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ProcessStartResult represents the result of a process start operation
type ProcessStartResult struct {
	ProcessStarted          bool // Process found in system
	GABPConnected           bool // Successfully connected to GABP server
	GameStillRunning        bool // Whether the game still looked alive after GABP attempt
	ProcessExitedDuringGABP bool // Process died before GABP became available
	GABPConnectError        error
	GABPConnectWait         time.Duration
	Error                   error
}

// SerializedStarter ensures only one process is starting at a time
// This implements the serialized starting approach requested by @pardeike
type SerializedStarter struct {
	mu                  sync.Mutex
	processStartTimeout time.Duration
	gabpConnectTimeout  time.Duration
}

// NewSerializedStarter creates a new serialized starter with default timeouts
func NewSerializedStarter() *SerializedStarter {
	return &SerializedStarter{
		processStartTimeout: 10 * time.Second, // Time to wait for process to appear in system
		gabpConnectTimeout:  10 * time.Second, // Short startup window; use games.connect later if the mod loads slowly
	}
}

// NewSerializedStarterForTesting creates a serialized starter with shorter timeouts for testing
func NewSerializedStarterForTesting() *SerializedStarter {
	return &SerializedStarter{
		processStartTimeout: 3 * time.Second, // Shorter timeout for tests
		gabpConnectTimeout:  2 * time.Second, // Much shorter GABP timeout for tests
	}
}

// StartWithVerification starts a process with full verification
// This method serializes the starting process as requested by @pardeike
func (s *SerializedStarter) StartWithVerification(
	controller ControllerInterface,
	gabpConnector GABPConnector,
	gameID string,
	port int,
	token string,
) *ProcessStartResult {
	result := &ProcessStartResult{}

	// Phase 1 & 2: Serialize the critical process starting and verification
	// Only hold the lock for the environment setup and process starting
	s.mu.Lock()

	// Phase 1: Start the process
	if err := controller.Start(); err != nil {
		s.mu.Unlock() // Release lock before returning
		result.Error = &ProcessError{
			Type:    ProcessErrorTypeStart,
			Context: fmt.Sprintf("failed to start process for %s", gameID),
			Err:     err,
		}
		return result
	}

	// Phase 2: Wait for process to be detectable in system
	// This is important for launcher-based games where there's a delay
	if controller, ok := controller.(*Controller); ok {
		if err := controller.WaitForProcessStart(s.processStartTimeout); err != nil {
			s.mu.Unlock() // Release lock before returning
			// Process didn't start or isn't detectable
			result.Error = err
			return result
		}
	}

	// If we reach here, the process is started and detectable
	result.ProcessStarted = true

	// Release the serialization lock - GABP connection can happen concurrently
	s.mu.Unlock()

	// Phase 3: Attempt GABP connection (NOT serialized - can happen concurrently)
	// This doesn't need to be serialized since it doesn't affect environment variables
	// and multiple GABP connections can be attempted simultaneously
	if gabpConnector != nil {
		gabpResult := s.attemptGABPConnection(controller, gabpConnector, gameID, port, token)
		result.GABPConnected = gabpResult.Connected
		result.GABPConnectError = gabpResult.Error
		result.GABPConnectWait = gabpResult.Waited
		result.GameStillRunning = gabpResult.GameStillRunning
		result.ProcessExitedDuringGABP = gabpResult.ProcessExitedDuringGABP
	} else {
		result.GameStillRunning = controllerLooksAlive(controller)
	}

	return result
}

type gabpConnectAttemptResult struct {
	Connected               bool
	Error                   error
	Waited                  time.Duration
	GameStillRunning        bool
	ProcessExitedDuringGABP bool
}

// attemptGABPConnection tries to establish GABP connection with timeout
func (s *SerializedStarter) attemptGABPConnection(
	controller ControllerInterface,
	connector GABPConnector,
	gameID string,
	port int,
	token string,
) gabpConnectAttemptResult {
	startedAt := time.Now()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	timeoutCtx, timeoutCancel := context.WithTimeoutCause(ctx, s.gabpConnectTimeout,
		fmt.Errorf("no GABP server became available within %s", s.gabpConnectTimeout))
	defer timeoutCancel()

	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)

		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeoutCtx.Done():
				return
			case <-ticker.C:
				if !controllerLooksAlive(controller) {
					cancel(fmt.Errorf("game process exited before GABP became available"))
					return
				}
			}
		}
	}()

	err := connector.AttemptConnection(timeoutCtx, gameID, port, token)
	<-monitorDone

	gameStillRunning := controllerLooksAlive(controller)
	result := gabpConnectAttemptResult{
		Connected:        err == nil,
		Error:            err,
		Waited:           time.Since(startedAt),
		GameStillRunning: gameStillRunning,
	}
	if err != nil && !gameStillRunning {
		result.ProcessExitedDuringGABP = true
	}

	return result
}

// SetTimeouts allows customization of timeout values
func (s *SerializedStarter) SetTimeouts(processStart, gabpConnect time.Duration) {
	s.processStartTimeout = processStart
	s.gabpConnectTimeout = gabpConnect
}

// GABPConnector interface for testing and abstraction
type GABPConnector interface {
	AttemptConnection(ctx context.Context, gameID string, port int, token string) error
}

func controllerLooksAlive(controller ControllerInterface) bool {
	if controller == nil {
		return false
	}

	if controller.IsRunning() {
		return true
	}

	switch controller.GetLaunchMode() {
	case "SteamAppId", "EpicAppId":
		return controller.IsLauncherProcessRunning()
	default:
		return false
	}
}
