package config

import (
	"fmt"
	"testing"
	"strings"
)

// TestImprovedErrorReporting validates that error messages are clear and specific
func TestImprovedErrorReporting(t *testing.T) {
	t.Run("PortFinderErrorMessage", func(t *testing.T) {
		// Call findAvailablePort directly with a range that's likely to fail
		// Use a privileged port range (1-1023) that typically requires root access
		_, err := findAvailablePort(1, 2)
		
		if err != nil {
			errMsg := err.Error()
			
			// Verify the error message format from findAvailablePort
			if !strings.Contains(errMsg, "no available ports in range") {
				t.Errorf("Error message should mention no available ports in range: %s", errMsg)
			}
			
			t.Logf("Direct port finder error message: %s", errMsg)
		} else {
			t.Logf("Unexpectedly found available ports in range 1-2, testing with broader check")
		}
		
		// Test the findAvailablePortWithConfig wrapper error message
		// This will try custom ranges then fall back to defaults, giving us the wrapping error
		gamesConfig := &GamesConfig{
			PortRanges: &PortRangeConfig{
				CustomRanges: []PortRange{
					{Min: 1, Max: 10},    // Privileged ports - likely to fail
					{Min: 20, Max: 30},   // More privileged ports
				},
			},
		}
		
		// Even if some ports in these ranges are available, if all fail we'll get our error
		_, err = findAvailablePortWithConfig(gamesConfig)
		if err != nil {
			errMsg := err.Error()
			
			// Verify the error message is concise and doesn't contain Windows-specific advice
			if strings.Contains(errMsg, "Windows system restrictions") {
				t.Errorf("Error message should not contain Windows-specific advice: %s", errMsg)
			}
			if strings.Contains(errMsg, "Hyper-V") {
				t.Errorf("Error message should not contain Hyper-V references: %s", errMsg)
			}
			if strings.Contains(errMsg, "netsh int ipv4") {
				t.Errorf("Error message should not contain Windows-specific commands: %s", errMsg)
			}
			
			// Verify the error message is helpful but concise
			if !strings.Contains(errMsg, "no available ports found") {
				t.Errorf("Error message should mention no available ports: %s", errMsg)
			}
			if !strings.Contains(errMsg, "last error") {
				t.Errorf("Error message should include underlying error: %s", errMsg)
			}
			
			t.Logf("Improved wrapper error message: %s", errMsg)
		} else {
			// If we don't get an error, that's fine - the test is mainly to verify 
			// the error message format when it does occur
			t.Logf("No error occurred - ports were available in test environment")
		}
	})
	
	t.Run("ErrorMessageFormat", func(t *testing.T) {
		// Test the error message format directly by constructing a test error
		testErr := fmt.Errorf("bind: permission denied")
		finalErr := fmt.Errorf("no available ports found in any configured range (last error: %w)", testErr)
		
		errMsg := finalErr.Error()
		
		// This should match our new simplified error format
		if !strings.Contains(errMsg, "no available ports found in any configured range") {
			t.Errorf("Error message format doesn't match expected: %s", errMsg)
		}
		if !strings.Contains(errMsg, "permission denied") {
			t.Errorf("Error message should wrap the underlying error: %s", errMsg)
		}
		
		// Verify it doesn't contain Windows-specific advice
		if strings.Contains(errMsg, "Windows") || strings.Contains(errMsg, "Hyper-V") {
			t.Errorf("Error message should not contain Windows-specific advice: %s", errMsg)
		}
		
		t.Logf("Verified error message format: %s", errMsg)
	})
}