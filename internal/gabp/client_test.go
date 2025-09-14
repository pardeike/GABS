package gabp

import (
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/util"
)

// testLogger implements util.Logger for testing
type testLogger struct{}

func (l *testLogger) Debugw(msg string, keysAndValues ...interface{}) {}
func (l *testLogger) Infow(msg string, keysAndValues ...interface{})  {}
func (l *testLogger) Warnw(msg string, keysAndValues ...interface{})  {}
func (l *testLogger) Errorw(msg string, keysAndValues ...interface{}) {}

func newTestLogger() util.Logger {
	return &testLogger{}
}

func TestExponentialBackoffWithJitter(t *testing.T) {
	tests := []struct {
		name           string
		attempt        int
		backoffMin     time.Duration
		backoffMax     time.Duration
		expectedMinMs  int64
		expectedMaxMs  int64
		allowJitter    bool
	}{
		{
			name:           "First attempt should be minimum backoff",
			attempt:        0,
			backoffMin:     100 * time.Millisecond,
			backoffMax:     30 * time.Second,
			expectedMinMs:  50,  // 50% jitter minimum
			expectedMaxMs:  150, // 150% jitter maximum
			allowJitter:    true,
		},
		{
			name:           "Second attempt should be doubled",
			attempt:        1,
			backoffMin:     100 * time.Millisecond,
			backoffMax:     30 * time.Second,
			expectedMinMs:  100, // 50% of 200ms
			expectedMaxMs:  300, // 150% of 200ms
			allowJitter:    true,
		},
		{
			name:           "Third attempt should cap at maximum",
			attempt:        10, // Large attempt to test capping
			backoffMin:     100 * time.Millisecond,
			backoffMax:     1 * time.Second,
			expectedMinMs:  500,  // 50% of 1000ms
			expectedMaxMs:  1500, // 150% of 1000ms
			allowJitter:    true,
		},
		{
			name:           "Zero jitter should return exact exponential value",
			attempt:        2,
			backoffMin:     100 * time.Millisecond,
			backoffMax:     30 * time.Second,
			expectedMinMs:  400, // Exactly 400ms for attempt 2
			expectedMaxMs:  400, // No jitter
			allowJitter:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := calculateExponentialBackoffWithJitter(tt.attempt, tt.backoffMin, tt.backoffMax, tt.allowJitter)
			delayMs := delay.Milliseconds()

			if delayMs < tt.expectedMinMs || delayMs > tt.expectedMaxMs {
				t.Errorf("calculateExponentialBackoffWithJitter() = %v (%dms), want between %dms and %dms",
					delay, delayMs, tt.expectedMinMs, tt.expectedMaxMs)
			}

			// Test that jitter is actually being applied when enabled
			if tt.allowJitter && tt.expectedMinMs != tt.expectedMaxMs {
				// Run multiple times to check for variation
				delays := make([]time.Duration, 10)
				for i := 0; i < 10; i++ {
					delays[i] = calculateExponentialBackoffWithJitter(tt.attempt, tt.backoffMin, tt.backoffMax, tt.allowJitter)
				}
				
				// Check that we get some variation (not all delays are identical)
				allSame := true
				for i := 1; i < len(delays); i++ {
					if delays[i] != delays[0] {
						allSame = false
						break
					}
				}
				
				if allSame {
					t.Logf("Note: All jittered delays were identical (%v) - this can happen but is statistically unlikely", delays[0])
				}
			}
		})
	}
}

func TestConnectWithExponentialBackoff(t *testing.T) {
	// Create a mock logger
	log := newTestLogger()
	client := NewClient(log)

	// Test connection to a non-existent server to trigger backoff
	// This should fail quickly but demonstrate the backoff behavior
	start := time.Now()
	err := client.Connect("127.0.0.1:0", "test-token", 50*time.Millisecond, 500*time.Millisecond)
	elapsed := time.Since(start)

	// Should fail (no server at port 0)
	if err == nil {
		t.Error("Expected connection to fail to non-existent server")
	}

	// Should have taken at least some time due to backoff retries
	// With 5 attempts and exponential backoff starting at 50ms, we expect:
	// Attempt 0: 50ms, Attempt 1: 100ms, Attempt 2: 200ms, Attempt 3: 400ms, Attempt 4: 500ms (capped)
	// Total minimum time (without jitter): ~1.25 seconds
	// With jitter, it could be 50% less: ~625ms
	minExpectedTime := 300 * time.Millisecond // Be lenient for CI environments
	
	if elapsed < minExpectedTime {
		t.Errorf("Expected connection attempts to take at least %v, but took %v", minExpectedTime, elapsed)
	}

	// Should not take too long either (max 5 attempts with capped backoff)
	maxExpectedTime := 5 * time.Second // Generous upper bound
	if elapsed > maxExpectedTime {
		t.Errorf("Expected connection attempts to take less than %v, but took %v", maxExpectedTime, elapsed)
	}
}

// calculateExponentialBackoffWithJitter computes the delay for exponential backoff with jitter
// This function is now implemented in client.go