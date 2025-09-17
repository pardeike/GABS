package process

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ProcessStartResult represents the result of a process start operation
type ProcessStartResult struct {
	ProcessStarted bool // Process found in system
	GABPConnected  bool // Successfully connected to GABP server
	Error          error
}

// SerializedStarter ensures only one process is starting at a time
// This implements the serialized starting approach requested by @pardeike
type SerializedStarter struct {
	mu                sync.Mutex
	processStartTimeout time.Duration
	gabpConnectTimeout  time.Duration
}

// NewSerializedStarter creates a new serialized starter with default timeouts
func NewSerializedStarter() *SerializedStarter {
	return &SerializedStarter{
		processStartTimeout: 10 * time.Second, // Time to wait for process to appear in system
		gabpConnectTimeout:  30 * time.Second, // Time to wait for GABP connection
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
	s.mu.Lock()
	defer s.mu.Unlock()

	result := &ProcessStartResult{}

	// Phase 1: Start the process
	if err := controller.Start(); err != nil {
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
			// Process didn't start or isn't detectable
			result.Error = err
			return result
		}
	}

	// If we reach here, the process is started and detectable
	result.ProcessStarted = true

	// Phase 3: Attempt GABP connection (if we have a connector)
	if gabpConnector != nil {
		connected := s.attemptGABPConnection(gabpConnector, gameID, port, token)
		result.GABPConnected = connected
		
		// Note: GABP connection failure is not considered an error for the process start
		// The process is running, we just can't control it via GABP
	}

	return result
}

// attemptGABPConnection tries to establish GABP connection with timeout
func (s *SerializedStarter) attemptGABPConnection(
	connector GABPConnector,
	gameID string,
	port int,
	token string,
) bool {
	ctx, cancel := context.WithTimeout(context.Background(), s.gabpConnectTimeout)
	defer cancel()

	// Create a channel to receive connection result
	connected := make(chan bool, 1)

	go func() {
		success := connector.AttemptConnection(gameID, port, token)
		select {
		case connected <- success:
		case <-ctx.Done():
		}
	}()

	select {
	case success := <-connected:
		return success
	case <-ctx.Done():
		// Timeout - GABP connection failed but process might still be running
		return false
	}
}

// SetTimeouts allows customization of timeout values
func (s *SerializedStarter) SetTimeouts(processStart, gabpConnect time.Duration) {
	s.processStartTimeout = processStart
	s.gabpConnectTimeout = gabpConnect
}

// GABPConnector interface for testing and abstraction
type GABPConnector interface {
	AttemptConnection(gameID string, port int, token string) bool
}