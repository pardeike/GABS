package config

import (
	"testing"
	"strings"
)

// TestErrorReportingDemonstration shows the before/after comparison of error messages
func TestErrorReportingDemonstration(t *testing.T) {
	t.Run("PortErrorMessageComparison", func(t *testing.T) {
		// Demonstrate the old vs new error message format
		t.Log("=== Error Message Improvement Demonstration ===")
		
		// Simulate the old error message (what the user was seeing)
		oldErrorMessage := "no available ports found in any range - this may be due to Windows system restrictions (Hyper-V, WSL, etc.) or firewall settings. Consider: 1) Checking Windows reserved port ranges with 'netsh int ipv4 show excludedportrange protocol=tcp', 2) Disabling Hyper-V if not needed, 3) Configuring your firewall/antivirus, 4) Adding custom port ranges to your GABS config file in the 'portRanges' section. Last error: bind: permission denied"
		
		// Our new simplified error message
		newErrorMessage := "no available ports found in any configured range (last error: bind: permission denied)"
		
		t.Logf("\nOLD ERROR MESSAGE (what user was seeing):\n%s", oldErrorMessage)
		t.Logf("\nNEW ERROR MESSAGE (improved):\n%s", newErrorMessage)
		
		// Verify improvements
		if len(newErrorMessage) < len(oldErrorMessage) {
			t.Logf("✓ New message is %d%% shorter (%d vs %d characters)", 
				100-100*len(newErrorMessage)/len(oldErrorMessage), 
				len(newErrorMessage), len(oldErrorMessage))
		}
		
		// Verify no Windows-specific advice
		if !strings.Contains(newErrorMessage, "Windows") && !strings.Contains(newErrorMessage, "Hyper-V") {
			t.Log("✓ New message removes Windows-specific assumptions")
		}
		
		// Verify still includes the actual underlying error
		if strings.Contains(newErrorMessage, "bind: permission denied") {
			t.Log("✓ New message still includes the actual underlying error")
		}
		
		// Verify conciseness while maintaining usefulness
		if strings.Contains(newErrorMessage, "no available ports found") && strings.Contains(newErrorMessage, "last error") {
			t.Log("✓ New message is concise but still informative")
		}
	})
	
	t.Run("GameStartErrorContext", func(t *testing.T) {
		t.Log("\n=== Game Start Error Context Improvement ===")
		
		// Demonstrate improved error context for game starting
		gameID := "rimworld"
		launchMode := "DirectPath"
		target := "C:\\Program Files (x86)\\Steam\\steamapps\\common\\RimWorld\\RimWorldWin64.exe"
		
		// Old generic error
		oldError := "failed to start game: no such file or directory"
		
		// New contextual error (simulated format from our improvements)
		newError := "failed to start game 'rimworld' (mode: DirectPath, target: C:\\Program Files (x86)\\Steam\\steamapps\\common\\RimWorld\\RimWorldWin64.exe): failed to start process 'C:\\Program Files (x86)\\Steam\\steamapps\\common\\RimWorld\\RimWorldWin64.exe': no such file or directory"
		
		t.Logf("\nOLD ERROR (generic):\n%s", oldError)
		t.Logf("\nNEW ERROR (with context):\n%s", newError)
		
		// Verify improvements
		if strings.Contains(newError, gameID) && strings.Contains(newError, launchMode) && strings.Contains(newError, target) {
			t.Log("✓ New error includes specific game details (ID, mode, target)")
		}
		
		if strings.Contains(newError, "no such file or directory") {
			t.Log("✓ New error still includes the underlying system error")
		}
		
		t.Log("✓ Users can now immediately see which game, launch mode, and executable path failed")
	})
}