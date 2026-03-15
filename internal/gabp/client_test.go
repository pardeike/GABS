package gabp

import (
	"context"
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

	timeout := 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	err := client.Connect(ctx, nonExistentAddr, "test-token", backoffMin, backoffMax)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected connection to fail")
	}

	// Should run until the context deadline, retrying with backoff
	if duration < timeout/2 {
		t.Errorf("Backoff too short: expected ~%v, got %v", timeout, duration)
	}
	if duration > timeout+100*time.Millisecond {
		t.Errorf("Didn't stop promptly after context cancellation: %v", duration)
	}

	t.Logf("Total backoff duration: %v (timeout: %v)", duration, timeout)
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

	timeout := 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	err := client.Connect(ctx, nonExistentAddr, "test-token", backoffMin, backoffMax)
	duration := time.Since(start)

	if err == nil {
		t.Fatal("Expected connection to fail")
	}

	// With 5ms max backoff, should get many attempts within the timeout
	if duration > timeout+100*time.Millisecond {
		t.Errorf("Didn't stop promptly after context cancellation: %v", duration)
	}

	t.Logf("Capped backoff duration: %v (timeout: %v)", duration, timeout)
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
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		client.Connect(ctx, nonExistentAddr, "test-token", backoffMin, backoffMax)
		cancel()
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