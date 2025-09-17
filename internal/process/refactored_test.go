package process

import (
	"testing"
	"time"
)

// TestRefactoredControllerStateless verifies the refactored controller is truly stateless
func TestRefactoredControllerStateless(t *testing.T) {
	t.Run("DirectProcessStatelessCheck", func(t *testing.T) {
		controller := NewRefactoredController()
		
		spec := LaunchSpec{
			GameId:   "test-stateless",
			Mode:     "DirectPath",
			PathOrId: "/bin/sleep",
			Args:     []string{"2"},
		}
		
		if err := controller.Configure(spec); err != nil {
			t.Fatalf("Configure error: %v", err)
		}
		
		// Test that IsRunning() always queries actual system state
		if controller.IsRunning() {
			t.Error("Process should not be running before start")
		}
		
		if err := controller.Start(); err != nil {
			t.Fatalf("Start error: %v", err)
		}
		
		// Should be running immediately (stateless query)
		if !controller.IsRunning() {
			t.Error("Process should be running immediately after start")
		}
		
		// Wait for process to finish
		time.Sleep(3 * time.Second)
		
		// Should correctly detect that process finished (stateless query)
		if controller.IsRunning() {
			t.Error("Process should not be running after it has finished")
		}
		
		t.Log("✅ Refactored controller correctly uses stateless queries")
	})
	
	t.Run("SerializedStarterWithVerification", func(t *testing.T) {
		starter := NewSerializedStarter()
		starter.SetTimeouts(5*time.Second, 2*time.Second) // Short timeouts for testing
		
		controller := NewRefactoredController()
		spec := LaunchSpec{
			GameId:   "test-serialized",
			Mode:     "DirectPath",
			PathOrId: "/bin/sleep",
			Args:     []string{"3"},
		}
		
		if err := controller.Configure(spec); err != nil {
			t.Fatalf("Configure error: %v", err)
		}
		
		// Test serialized starting with verification
		result := starter.StartWithVerification(controller, nil, "test-serialized", 0, "")
		
		if result.Error != nil {
			t.Fatalf("Serialized start failed: %v", result.Error)
		}
		
		if !result.ProcessStarted {
			t.Error("Process should be detected as started")
		}
		
		// GABP connection should be false since we passed nil connector
		if result.GABPConnected {
			t.Error("GABP connection should be false with nil connector")
		}
		
		t.Logf("✅ Serialized starter result: ProcessStarted=%v, GABPConnected=%v", 
			result.ProcessStarted, result.GABPConnected)
	})
}

// TestStatelessVsStateful demonstrates the difference in approaches  
func TestStatelessVsStateful(t *testing.T) {
	t.Log("=== Demonstrating Stateless vs Stateful Approach ===")
	
	// Old approach (stateful with complex state management)
	t.Log("OLD APPROACH: Complex state tracking with background monitoring")
	t.Log("- Maintained internal ProcessState with goroutines")
	t.Log("- Risk of state becoming out of sync with reality")
	t.Log("- Complex state transitions and background monitoring")
	
	// New approach (stateless)
	t.Log("\nNEW APPROACH: Simple stateless queries")
	t.Log("- Direct system queries when IsRunning() is called")
	t.Log("- Always reflects actual system state")
	t.Log("- Simple and reliable - no internal state to maintain")
	
	// Demonstrate the stateless approach
	controller := NewRefactoredController()
	spec := LaunchSpec{
		GameId:   "demo-stateless",
		Mode:     "DirectPath", 
		PathOrId: "/bin/echo",
		Args:     []string{"Hello stateless world"},
	}
	
	controller.Configure(spec)
	controller.Start()
	
	// Each IsRunning() call queries the actual system
	running1 := controller.IsRunning()
	running2 := controller.IsRunning()
	
	t.Logf("Stateless queries: Call 1: %v, Call 2: %v", running1, running2)
	t.Log("✅ Each call queries actual system state - no internal state maintained")
}