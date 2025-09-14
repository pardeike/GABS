package config

import (
	"sync"
	"testing"
	"time"
)

func TestConcurrentBridgeCreation(t *testing.T) {
	t.Log("Testing concurrent bridge file creation for different games...")

	var wg sync.WaitGroup
	results := make([]concurrentResult, 10)
	gameNames := []string{"minecraft", "rimworld", "terraria", "stardew", "factorio", "cities", "valheim", "subnautica", "kerbal", "oxygen"}

	// Launch 10 concurrent bridge creations for different games
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			start := time.Now()
			port, token, path, err := WriteBridgeJSON(gameNames[index], "")
			duration := time.Since(start)

			results[index] = concurrentResult{
				Index:    index,
				Port:     port,
				Token:    token,
				Path:     path,
				Error:    err,
				Duration: duration,
				GameName: gameNames[index],
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
			t.Logf("  %s: ERROR - %v", r.GameName, r.Error)
			errorCount++
		} else {
			t.Logf("  %s: Port=%d, Token=%s..., Path=%s, Duration=%v",
				r.GameName, r.Port, r.Token[:8], r.Path, r.Duration)

			if portMap[r.Port] {
				t.Errorf("Port %d was used by multiple games! This indicates a concurrency issue.", r.Port)
			}
			portMap[r.Port] = true

			if pathMap[r.Path] {
				t.Errorf("Path %s was used by multiple games! Each game should have its own directory.", r.Path)
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

	// This test verifies that concurrent launches of DIFFERENT games work correctly:
	// - Each game gets a unique port (no conflicts)
	// - Each game gets its own bridge file path (in its own directory)
	// - No errors due to race conditions
	// - ENV variable isolation is guaranteed by Go's exec.Command
}

type concurrentResult struct {
	Index    int
	Port     int
	Token    string
	Path     string
	Error    error
	Duration time.Duration
	GameName string
}
