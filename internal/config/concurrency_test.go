package config

import (
	"sync"
	"testing"
	"time"
)

func TestConcurrentBridgeCreation(t *testing.T) {
	t.Log("Testing concurrent bridge file creation for same game...")
	
	var wg sync.WaitGroup
	results := make([]concurrentResult, 10)
	
	// Launch 10 concurrent bridge creations for the same game
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			
			start := time.Now()
			port, token, path, err := WriteBridgeJSON("minecraft", "")
			duration := time.Since(start)
			
			results[index] = concurrentResult{
				Index:    index,
				Port:     port, 
				Token:    token,
				Path:     path,
				Error:    err,
				Duration: duration,
			}
		}(i)
	}
	
	wg.Wait()
	
	// Check results
	portMap := make(map[int]bool)
	pathMap := make(map[string]bool)
	errorCount := 0
	
	for _, r := range results {
		if r.Error != nil {
			t.Logf("  %d: ERROR - %v", r.Index, r.Error)
			errorCount++
		} else {
			t.Logf("  %d: Port=%d, Token=%s..., Path=%s, Duration=%v", 
				r.Index, r.Port, r.Token[:8], r.Path, r.Duration)
			
			if portMap[r.Port] {
				t.Errorf("Port %d was used by multiple launches! This indicates a concurrency issue.", r.Port)
			}
			portMap[r.Port] = true
			
			if pathMap[r.Path] {
				// This is expected - we want unique paths for concurrent launches
				t.Logf("    Note: Path %s is unique (this is good!)", r.Path)
			} else {
				pathMap[r.Path] = true
			}
		}
	}
	
	// Verify results
	if errorCount > 0 {
		t.Errorf("Expected no errors in concurrent bridge creation, got %d errors", errorCount)
	}
	
	if len(portMap) != (10 - errorCount) {
		t.Errorf("Expected %d unique ports, got %d", 10-errorCount, len(portMap))
	}
	
	if len(pathMap) != (10 - errorCount) {
		t.Errorf("Expected %d unique paths, got %d", 10-errorCount, len(pathMap))
	}
	
	t.Logf("Summary: %d unique ports, %d unique paths, %d errors", 
		len(portMap), len(pathMap), errorCount)
	
	// This test should pass if concurrency is handled correctly:
	// - Each launch gets a unique port (no conflicts)
	// - Each launch gets a unique bridge file path (concurrent-safe)
	// - No errors due to race conditions
}

type concurrentResult struct {
	Index    int
	Port     int
	Token    string
	Path     string
	Error    error
	Duration time.Duration
}