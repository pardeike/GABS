package process

import (
	"os/exec"
	"testing"
	"time"
)

// TestImprovedControllerStatusReporting tests the improved status reporting
func TestImprovedControllerStatusReporting(t *testing.T) {
	t.Run("DirectProcessLifecycle", func(t *testing.T) {
		controller := NewImprovedController()
		defer controller.Cleanup()
		
		spec := LaunchSpec{
			GameId:   "test-direct-improved",
			Mode:     "DirectPath",
			PathOrId: "/bin/sleep",
			Args:     []string{"2"},
		}
		
		if err := controller.Configure(spec); err != nil {
			t.Fatalf("Configure error: %v", err)
		}
		
		// Initial state should be stopped
		if controller.IsRunning() {
			t.Error("Process should not be running initially")
		}
		if controller.GetState() != ProcessStateStopped {
			t.Errorf("Initial state should be stopped, got %s", controller.GetState())
		}
		
		t.Log("Starting process...")
		if err := controller.Start(); err != nil {
			t.Fatalf("Start error: %v", err)
		}
		
		// Should be running immediately after start
		if !controller.IsRunning() {
			t.Error("Process should be running immediately after start")
		}
		
		pid := controller.GetPID()
		if pid <= 0 {
			t.Error("PID should be positive after start")
		}
		t.Logf("Process started with PID: %d", pid)
		
		// Wait a bit, should still be running
		time.Sleep(100 * time.Millisecond)
		if !controller.IsRunning() {
			t.Error("Process should still be running after 100ms")
		}
		
		// Wait for process to finish naturally
		t.Log("Waiting for process to finish...")
		time.Sleep(3 * time.Second)
		
		// Should be stopped now
		if controller.IsRunning() {
			t.Error("Process should be stopped after it has finished")
		}
		if controller.GetState() != ProcessStateStopped {
			t.Errorf("State should be stopped after process finish, got %s", controller.GetState())
		}
		
		t.Log("✅ Direct process lifecycle works correctly")
	})
	
	t.Run("SteamProcessWithTracking", func(t *testing.T) {
		controller := NewImprovedController()
		defer controller.Cleanup()
		
		spec := LaunchSpec{
			GameId:          "test-steam-improved",
			Mode:            "SteamAppId",
			PathOrId:        "123456",
			StopProcessName: "sleep", // For testing, we'll look for sleep processes
		}
		
		if err := controller.Configure(spec); err != nil {
			t.Fatalf("Configure error: %v", err)
		}
		
		// Start a background sleep process to simulate a game
		cmd := exec.Command("sleep", "5")
		if err := cmd.Start(); err != nil {
			t.Fatalf("Failed to start background process: %v", err)
		}
		defer cmd.Process.Kill()
		
		t.Logf("Started background sleep process (PID: %d) to simulate game", cmd.Process.Pid)
		
		// Start the improved controller's Steam launcher (will fail but that's ok)
		t.Log("Starting Steam controller (launcher will fail, but monitoring should work)...")
		err := controller.Start()
		t.Logf("Steam controller start result: %v", err)
		
		// Give some time for the monitoring to detect the process
		time.Sleep(1 * time.Second)
		
		t.Logf("Controller state after start: %s", controller.GetState())
		t.Logf("Controller IsRunning: %v", controller.IsRunning())
		
		// This should find the sleep process
		if !controller.IsRunning() {
			t.Error("Steam process should show as running when StopProcessName process exists")
		}
		
		// Kill the background process
		cmd.Process.Kill()
		cmd.Wait()
		
		// Give monitoring time to detect the process has stopped
		time.Sleep(3 * time.Second)
		
		if controller.IsRunning() {
			t.Error("Steam process should show as stopped when StopProcessName process no longer exists")
		}
		
		t.Log("✅ Steam process tracking works correctly")
	})
	
	t.Run("SteamProcessWithoutTracking", func(t *testing.T) {
		controller := NewImprovedController()
		defer controller.Cleanup()
		
		spec := LaunchSpec{
			GameId:   "test-steam-no-tracking",
			Mode:     "SteamAppId",
			PathOrId: "123456",
			// No StopProcessName - can't track the actual game
		}
		
		if err := controller.Configure(spec); err != nil {
			t.Fatalf("Configure error: %v", err)
		}
		
		t.Log("This test shows improved handling of untrackable Steam games")
		
		// The launcher command will likely fail, but that's expected
		// We want to see the state handling
		state := controller.GetState()
		t.Logf("Initial state: %s", state)
		
		if controller.IsRunning() {
			t.Error("Steam process without tracking should not report as running initially")
		}
		
		t.Log("✅ Steam process without tracking handled correctly")
	})
	
	t.Run("ErrorHandling", func(t *testing.T) {
		controller := NewImprovedController()
		defer controller.Cleanup()
		
		// Test configuration error
		spec := LaunchSpec{
			// Missing GameId
			Mode:     "DirectPath",
			PathOrId: "/bin/echo",
		}
		
		err := controller.Configure(spec)
		if err == nil {
			t.Error("Configure should fail when GameId is missing")
		}
		
		processErr, ok := err.(*ProcessError)
		if !ok {
			t.Error("Error should be of type ProcessError")
		} else {
			if processErr.Type != ProcessErrorTypeConfiguration {
				t.Error("Error type should be ProcessErrorTypeConfiguration")
			}
			t.Logf("Configuration error correctly caught: %v", processErr)
		}
		
		// Test start error with invalid path
		spec.GameId = "test-error"
		spec.PathOrId = "/nonexistent/invalid/path"
		
		if err := controller.Configure(spec); err != nil {
			t.Fatalf("Configure should succeed: %v", err)
		}
		
		err = controller.Start()
		if err == nil {
			t.Error("Start should fail with invalid path")
		}
		
		processErr, ok = err.(*ProcessError)
		if !ok {
			t.Error("Start error should be of type ProcessError")
		} else {
			if processErr.Type != ProcessErrorTypeStart {
				t.Error("Error type should be ProcessErrorTypeStart")
			}
			t.Logf("Start error correctly caught: %v", processErr)
		}
		
		t.Log("✅ Error handling works correctly")
	})
}