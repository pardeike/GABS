package process

import (
	"testing"
	"strings"
)

// TestImprovedProcessErrorReporting validates that process controller error messages are specific
func TestImprovedProcessErrorReporting(t *testing.T) {
	t.Run("DirectPathStartError", func(t *testing.T) {
		controller := &Controller{}
		
		// Configure with an invalid executable path
		spec := LaunchSpec{
			GameId:   "test-game",
			Mode:     "DirectPath",
			PathOrId: "/nonexistent/invalid/path/to/game.exe",
		}
		
		err := controller.Configure(spec)
		if err != nil {
			t.Fatalf("Configure should not fail for valid spec: %v", err)
		}
		
		// Try to start the invalid process
		err = controller.Start()
		if err == nil {
			t.Fatal("Expected error when starting nonexistent process")
		}
		
		errMsg := err.Error()
		
		// Verify the error message includes the specific path that failed
		if !strings.Contains(errMsg, "/nonexistent/invalid/path/to/game.exe") {
			t.Errorf("Error message should include the specific path: %s", errMsg)
		}
		if !strings.Contains(errMsg, "failed to start process") {
			t.Errorf("Error message should mention process start failure: %s", errMsg)
		}
		
		t.Logf("Improved process error message: %s", errMsg)
	})
	
	t.Run("ConfigurationError", func(t *testing.T) {
		controller := &Controller{}
		
		// Configure with missing required fields
		spec := LaunchSpec{
			GameId:   "",  // Missing GameId
			Mode:     "DirectPath",
			PathOrId: "/some/path",
		}
		
		err := controller.Configure(spec)
		if err == nil {
			t.Fatal("Expected error when GameId is missing")
		}
		
		errMsg := err.Error()
		
		// Verify the error message is specific about what's missing
		if !strings.Contains(errMsg, "GameId") {
			t.Errorf("Error message should mention GameId requirement: %s", errMsg)
		}
		
		t.Logf("Configuration error message: %s", errMsg)
	})
}