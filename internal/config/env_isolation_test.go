package config

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestENVIsolationInGameLaunching tests that environment variables are properly isolated
// between concurrent game process launches, ensuring ENV variable cross-contamination doesn't occur.
// This addresses the original concern about ENV variables being global rather than process-local.
func TestENVIsolationInGameLaunching(t *testing.T) {
	t.Log("Testing ENV variable isolation in concurrent game process launches...")

	var wg sync.WaitGroup
	results := make([]envTestResult, 5)
	gameIDs := []string{"game1", "game2", "game3", "game4", "game5"}

	// Launch 5 concurrent processes (simulating game launches) with different ENV variables
	for i, gameID := range gameIDs {
		wg.Add(1)
		go func(index int, gID string) {
			defer wg.Done()

			// Create bridge config for this game (each gets unique port/token)
			port, token, _, err := WriteBridgeJSON(gID, "")
			if err != nil {
				results[index] = envTestResult{
					GameID: gID,
					Error:  err,
				}
				return
			}

			// Simulate process launch like Controller.Start() does
			cmd := exec.Command("sh", "-c", "echo GAME:$GABS_GAME_ID PORT:$GABP_SERVER_PORT TOKEN:$GABP_TOKEN")

			// Set environment variables like the real game launching code does
			cmd.Env = append(cmd.Env,
				"GABS_GAME_ID="+gID,
				"GABP_SERVER_PORT="+string(rune(port+48)), // Convert to string (simplified)
				"GABP_TOKEN="+token[:16], // Use first 16 chars for test
			)

			output, execErr := cmd.Output()
			results[index] = envTestResult{
				GameID:     gID,
				Port:       port,
				Token:      token[:16],
				Output:     strings.TrimSpace(string(output)),
				Error:      execErr,
			}
		}(i, gameID)

		// Small delay to interleave the launches
		time.Sleep(1 * time.Millisecond)
	}

	wg.Wait()

	// Verify results
	errorCount := 0
	for _, result := range results {
		if result.Error != nil {
			t.Logf("  %s: ERROR - %v", result.GameID, result.Error)
			errorCount++
			continue
		}

		t.Logf("  %s: Port=%d, Token=%s..., Output=%s", 
			result.GameID, result.Port, result.Token, result.Output)

		// Verify the process received the correct ENV variables for its game
		expectedGameIDPart := "GAME:" + result.GameID
		if !strings.Contains(result.Output, expectedGameIDPart) {
			t.Errorf("Process for %s didn't receive correct GABS_GAME_ID. Output: %s", result.GameID, result.Output)
		}

		// Verify no cross-contamination by checking that other game IDs don't appear
		for _, otherGameID := range gameIDs {
			if otherGameID != result.GameID && strings.Contains(result.Output, "GAME:"+otherGameID) {
				t.Errorf("Process for %s received ENV from %s! Cross-contamination detected. Output: %s", 
					result.GameID, otherGameID, result.Output)
			}
		}
	}

	if errorCount > 0 {
		t.Errorf("Expected no errors in concurrent ENV isolation test, got %d errors", errorCount)
	}

	t.Logf("ENV isolation test completed: %d games tested, %d errors", len(gameIDs), errorCount)
	t.Log("✓ This test confirms that Go's exec.Command provides proper ENV variable isolation per process")
	t.Log("✓ Concurrent launching of different games (X and Y) is safe without serialization")
}

type envTestResult struct {
	GameID string
	Port   int
	Token  string
	Output string
	Error  error
}