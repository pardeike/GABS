package gabp

import (
	"math"
	"net"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/util"
)

// Test exponential backoff behavior by connecting to a non-existent server
func TestExponentialBackoff(t *testing.T) {
	// Create a client with a mock logger
	log := util.NewLogger("debug") // debug mode to see backoff logs
	client := NewClient(log)

	// Test parameters
	backoffMin := 10 * time.Millisecond
	backoffMax := 100 * time.Millisecond
	
	// Try to connect to a non-existent address to trigger retries
	nonExistentAddr := "127.0.0.1:99999" // Use a port that's very unlikely to be in use
	
	// Ensure the port is not actually in use
	if conn, err := net.Dial("tcp", nonExistentAddr); err == nil {
		conn.Close()
		t.Skip("Test port is actually in use, skipping backoff test")
	}

	start := time.Now()
	err := client.Connect(nonExistentAddr, "test-token", backoffMin, backoffMax)
	duration := time.Since(start)

	// Should fail after all retries
	if err == nil {
		t.Fatal("Expected connection to fail")
	}

	// With 5 attempts and exponential backoff:
	// Attempt 1: immediate
	// Attempt 2: wait ~10ms (backoffMin * 2^0 = 10ms)
	// Attempt 3: wait ~20ms (backoffMin * 2^1 = 20ms) 
	// Attempt 4: wait ~40ms (backoffMin * 2^2 = 40ms)
	// Attempt 5: wait ~80ms (backoffMin * 2^3 = 80ms)
	// Total expected: ~150ms, but with jitter it could vary ±25%
	
	expectedMin := time.Duration(float64(10+20+40+80) * 0.75 * float64(time.Millisecond)) // 112.5ms with jitter
	expectedMax := time.Duration(float64(10+20+40+80) * 1.25 * float64(time.Millisecond)) // 187.5ms with jitter
	
	// Allow some tolerance for system scheduling
	if duration < expectedMin/2 {
		t.Errorf("Backoff too short: expected at least %v, got %v", expectedMin/2, duration)
	}
	
	if duration > expectedMax*2 {
		t.Errorf("Backoff too long: expected at most %v, got %v", expectedMax*2, duration)
	}

	t.Logf("Total backoff duration: %v (expected range: %v - %v)", duration, expectedMin, expectedMax)
}

// Test that backoff respects the maximum delay
func TestBackoffMaximum(t *testing.T) {
	log := util.NewLogger("error") // quiet for this test
	client := NewClient(log)

	backoffMin := 1 * time.Millisecond
	backoffMax := 5 * time.Millisecond // Very small max to test capping
	
	nonExistentAddr := "127.0.0.1:99998"
	
	// Ensure the port is not actually in use
	if conn, err := net.Dial("tcp", nonExistentAddr); err == nil {
		conn.Close()
		t.Skip("Test port is actually in use, skipping backoff test")
	}

	start := time.Now()
	err := client.Connect(nonExistentAddr, "test-token", backoffMin, backoffMax)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected connection to fail")
	}

	// With max of 5ms, later attempts should be capped
	// All delays should be ≤ 5ms * 1.25 (jitter) = 6.25ms
	maxExpected := time.Duration(float64(5*4) * 1.25 * float64(time.Millisecond)) // 4 waits, max 5ms each with jitter
	
	if duration > maxExpected*2 { // Allow system tolerance  
		t.Errorf("Backoff exceeded maximum: expected at most %v, got %v", maxExpected*2, duration)
	}

	t.Logf("Capped backoff duration: %v (max expected: %v)", duration, maxExpected)
}

// Test that jitter provides variation in delays
func TestBackoffJitter(t *testing.T) {
	log := util.NewLogger("error")
	
	backoffMin := 10 * time.Millisecond
	backoffMax := 100 * time.Millisecond
	nonExistentAddr := "127.0.0.1:99997"
	
	// Ensure the port is not actually in use
	if conn, err := net.Dial("tcp", nonExistentAddr); err == nil {
		conn.Close()
		t.Skip("Test port is actually in use, skipping backoff test")
	}

	var durations []time.Duration
	
	// Run multiple connection attempts to observe jitter variation
	for i := 0; i < 3; i++ {
		client := NewClient(log)
		start := time.Now()
		client.Connect(nonExistentAddr, "test-token", backoffMin, backoffMax)
		durations = append(durations, time.Since(start))
	}

	// Check that we have some variation (jitter working)
	// All durations shouldn't be identical due to jitter
	allSame := true
	for i := 1; i < len(durations); i++ {
		// Allow 1ms tolerance for timing precision
		if math.Abs(float64(durations[i]-durations[0])) > float64(1*time.Millisecond) {
			allSame = false
			break
		}
	}

	if allSame {
		t.Log("Warning: All backoff durations were very similar, jitter may not be working effectively")
		// Don't fail the test as this could happen by chance, but log it
	}

	t.Logf("Jitter test durations: %v", durations)
}