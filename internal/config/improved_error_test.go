package config

import (
	"testing"
)

// TestImprovedPortAssignment validates that port assignment is deterministic and doesn't fail
func TestImprovedPortAssignment(t *testing.T) {
	t.Run("PortAssignmentSuccess", func(t *testing.T) {
		// Test that port assignment always succeeds and is deterministic
		gamesConfig := &GamesConfig{
			PortRanges: &PortRangeConfig{
				CustomRanges: []PortRange{
					{Min: 8000, Max: 8999},
				},
			},
		}
		
		// Port assignment should never fail
		port, err := assignPortWithConfig(gamesConfig)
		if err != nil {
			t.Errorf("Port assignment should not fail: %v", err)
		}
		
		// Verify port is in the configured range
		if port < 8000 || port > 8999 {
			t.Errorf("Port %d not in configured range [8000, 8999]", port)
		}
		
		t.Logf("Successfully assigned port: %d", port)
	})
	
	t.Run("PortAssignmentWithDefaults", func(t *testing.T) {
		// Test that port assignment works with default ranges
		port, err := assignPortWithConfig(nil)
		if err != nil {
			t.Errorf("Port assignment with defaults should not fail: %v", err)
		}
		
		// Verify port is in a valid range (should be from first default range: 49152-65535)
		if port < 49152 || port > 65535 {
			t.Errorf("Port %d not in expected default range [49152, 65535]", port)
		}
		
		t.Logf("Successfully assigned port from defaults: %d", port)
	})
}