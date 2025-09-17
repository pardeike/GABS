package process

import (
	"os/exec"
	"testing"
	"time"
)

// TestStatusReportingIssues demonstrates the current status reporting problems
func TestStatusReportingIssues(t *testing.T) {
	t.Run("DirectProcessStatusReporting", func(t *testing.T) {
		controller := &Controller{}
		spec := LaunchSpec{
			GameId:   "test-direct",
			Mode:     "DirectPath",
			PathOrId: "/bin/sleep",
			Args:     []string{"2"},
		}
		
		if err := controller.Configure(spec); err != nil {
			t.Fatalf("Configure error: %v", err)
		}
		
		t.Log("Starting process...")
		if err := controller.Start(); err != nil {
			t.Fatalf("Start error: %v", err)
		}
		
		// Check status immediately
		isRunning := controller.IsRunning()
		pid := controller.GetPID()
		t.Logf("Status immediately: IsRunning=%v, PID=%d", isRunning, pid)
		
		if !isRunning {
			t.Error("Process should be running immediately after start")
		}
		
		// Wait a bit
		time.Sleep(100 * time.Millisecond)
		isRunning = controller.IsRunning()
		t.Logf("Status after 100ms: IsRunning=%v, PID=%d", isRunning, pid)
		
		if !isRunning {
			t.Error("Process should still be running after 100ms")
		}
		
		// Wait for process to finish
		time.Sleep(3 * time.Second)
		isRunning = controller.IsRunning()
		t.Logf("Status after 3s: IsRunning=%v, PID=%d", isRunning, pid)
		
		if isRunning {
			t.Error("Process should not be running after it has finished")
		}
	})
	
	t.Run("SteamProcessStatusReporting", func(t *testing.T) {
		controller := &Controller{}
		spec := LaunchSpec{
			GameId:          "test-steam",
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
		
		// This should find the sleep process
		isRunning := controller.IsRunning()
		t.Logf("Status with StopProcessName: IsRunning=%v", isRunning)
		
		if !isRunning {
			t.Error("Steam process should show as running when StopProcessName process exists")
		}
		
		// Test without StopProcessName - this shows the problem
		spec.StopProcessName = ""
		controller.Configure(spec)
		isRunning = controller.IsRunning()
		t.Logf("Status without StopProcessName: IsRunning=%v", isRunning)
		
		// This will always be false, which is the main status reporting issue
		if isRunning {
			t.Error("Steam process without StopProcessName should report false (expected behavior)")
		}
		
		// Clean up
		cmd.Process.Kill()
	})
}

// TestLauncherProcessBehavior shows how launcher processes behave differently
func TestLauncherProcessBehavior(t *testing.T) {
	t.Run("SteamLauncherQuickExit", func(t *testing.T) {
		controller := &Controller{}
		spec := LaunchSpec{
			GameId:   "test-steam-launcher",
			Mode:     "SteamAppId",
			PathOrId: "123456",
		}
		
		if err := controller.Configure(spec); err != nil {
			t.Fatalf("Configure error: %v", err)
		}
		
		t.Log("This test shows how Steam launcher processes exit quickly")
		t.Log("The launcher command will likely fail since we don't have Steam, but that's expected")
		
		// This will likely fail to start Steam, but we want to see the behavior
		err := controller.Start()
		t.Logf("Start result: %v", err)
		
		// Even if it fails, we can check the behavior
		isLauncherRunning := controller.IsLauncherProcessRunning()
		isRunning := controller.IsRunning()
		pid := controller.GetPID()
		
		t.Logf("IsLauncherProcessRunning: %v", isLauncherRunning)
		t.Logf("IsRunning: %v", isRunning)
		t.Logf("PID: %d", pid)
	})
}