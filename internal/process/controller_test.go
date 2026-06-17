package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/steam"
)

func TestBridgePathGeneration(t *testing.T) {
	controller := &Controller{}

	// Configure with test parameters
	spec := LaunchSpec{
		GameId:   "test-game",
		Mode:     "DirectPath",
		PathOrId: "echo",
		Args:     []string{"test"},
	}

	err := controller.Configure(spec)
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// Test getBridgePath method
	bridgePath := controller.getBridgePath()

	// Verify the path contains the expected components
	if !strings.Contains(bridgePath, ".gabs") {
		t.Errorf("Bridge path should contain '.gabs', got: %s", bridgePath)
	}

	if !strings.Contains(bridgePath, "test-game") {
		t.Errorf("Bridge path should contain game ID 'test-game', got: %s", bridgePath)
	}

	if !strings.HasSuffix(bridgePath, "bridge.json") {
		t.Errorf("Bridge path should end with 'bridge.json', got: %s", bridgePath)
	}

	// Test the expected path structure
	homeDir, err := os.UserHomeDir()
	if err == nil {
		expectedPath := filepath.Join(homeDir, ".gabs", "test-game", "bridge.json")
		if bridgePath != expectedPath {
			t.Errorf("Bridge path mismatch. Expected: %s, Got: %s", expectedPath, bridgePath)
		}
	}
}

func TestEnvironmentVariables(t *testing.T) {
	// Test that environment variables would be set correctly
	// Note: We can't actually start a process in tests without side effects,
	// but we can verify the logic would work correctly

	controller := &Controller{}

	spec := LaunchSpec{
		GameId:   "factory",
		Mode:     "DirectPath",
		PathOrId: "echo",
		Args:     []string{"test"},
	}

	err := controller.Configure(spec)
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// Set bridge info for testing
	controller.SetBridgeInfo(12345, "test-token-1234567890abcdef")

	// Verify the bridge path generation
	bridgePath := controller.getBridgePath()
	expectedGameId := "factory"

	if !strings.Contains(bridgePath, expectedGameId) {
		t.Errorf("Bridge path should contain game ID '%s', got: %s", expectedGameId, bridgePath)
	}

	// Test that bridge info is set correctly
	if controller.bridgeInfo == nil {
		t.Fatal("Bridge info should be set")
	}

	// Host is always 127.0.0.1 for GABS - no need to store it in bridgeInfo
	if controller.bridgeInfo.Port != 12345 {
		t.Errorf("Expected port 12345, got %d", controller.bridgeInfo.Port)
	}

	if controller.bridgeInfo.Token != "test-token-1234567890abcdef" {
		t.Errorf("Expected token test-token-1234567890abcdef, got %s", controller.bridgeInfo.Token)
	}

	// The environment variables that would be set are:
	// GABS_GAME_ID=factory
	// GABS_BRIDGE_PATH=<bridgePath>
	// GABP_SERVER_PORT=12345
	// GABP_TOKEN=test-token-1234567890abcdef

	t.Logf("GABS_GAME_ID would be set to: %s", expectedGameId)
	t.Logf("GABS_BRIDGE_PATH would be set to: %s", bridgePath)
	t.Logf("GABP_SERVER_PORT would be set to: %d", controller.bridgeInfo.Port)
	t.Logf("GABP_TOKEN would be set to: %s", controller.bridgeInfo.Token)
}

func TestSteamManagedStartUsesResolvedExecutableAndBridgeEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test creates Unix executable permissions")
	}

	tempDir := t.TempDir()
	library := filepath.Join(tempDir, "Steam")
	steamapps := filepath.Join(library, "steamapps")
	install := filepath.Join(steamapps, "common", "ExampleGame")
	exe := filepath.Join(install, "ExampleGame")

	writeTestFile(t, filepath.Join(steamapps, "libraryfolders.vdf"), `
		"libraryfolders"
		{
			"0" { "path" "`+library+`" }
		}
	`, 0644)
	writeTestFile(t, filepath.Join(steamapps, "appmanifest_123456.acf"), `
		"AppState"
		{
			"appid" "123456"
			"name" "Example Game"
			"installdir" "ExampleGame"
		}
	`, 0644)
	writeTestFile(t, exe, "#!/bin/sh\nsleep 30\n", 0755)

	t.Setenv("GABS_STEAM_LIBRARYFOLDERS", filepath.Join(steamapps, "libraryfolders.vdf"))
	restoreSteamControl := steam.SetClientControlForTesting(
		func() (string, []string, error) {
			return os.Args[0], []string{"-test.run=TestSteamClientStartHelper", "--"}, nil
		},
		func() bool { return true },
		0,
		0,
	)
	t.Cleanup(restoreSteamControl)

	controller := &Controller{}
	spec := LaunchSpec{
		GameId:   "steam-managed-test",
		Mode:     "SteamManaged",
		PathOrId: "123456",
	}
	if err := controller.Configure(spec); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	controller.SetBridgeInfo(43210, "managed-token")

	if err := controller.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Kill()
	})

	if controller.cmd == nil {
		t.Fatal("expected command to be started")
	}
	if controller.cmd.Path != exe {
		t.Fatalf("expected managed executable %s, got %s", exe, controller.cmd.Path)
	}
	if controller.cmd.Dir != install {
		t.Fatalf("expected working dir %s, got %s", install, controller.cmd.Dir)
	}

	wantEnv := []string{
		"GABS_GAME_ID=steam-managed-test",
		"GABP_SERVER_PORT=43210",
		"GABP_TOKEN=managed-token",
		"SteamAppId=123456",
		"SteamGameId=123456",
	}
	for _, want := range wantEnv {
		if !containsEnv(controller.cmd.Env, want) {
			t.Fatalf("expected env %s in %#v", want, controller.cmd.Env)
		}
	}

	appIDPath := filepath.Join(install, "steam_appid.txt")
	data, err := os.ReadFile(appIDPath)
	if err != nil {
		t.Fatalf("expected app id file: %v", err)
	}
	if strings.TrimSpace(string(data)) != "123456" {
		t.Fatalf("unexpected app id file content %q", string(data))
	}
}

func TestSteamClientStartHelper(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "--" {
			return
		}
	}
}

func TestLauncherWaitForProcessStartUsesStopProcessName(t *testing.T) {
	controller := &Controller{}
	spec := LaunchSpec{
		GameId:          "steam-test",
		Mode:            "SteamAppId",
		PathOrId:        "12345",
		StopProcessName: "Real Game Process",
	}

	if err := controller.Configure(spec); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	controller.waitDone = make(chan struct{})
	close(controller.waitDone)

	originalFinder := findProcessesByNameFunc
	findCalls := 0
	findProcessesByNameFunc = func(name string) ([]int, error) {
		findCalls++
		if name != spec.StopProcessName {
			t.Fatalf("expected lookup for %q, got %q", spec.StopProcessName, name)
		}
		return []int{1234}, nil
	}
	t.Cleanup(func() {
		findProcessesByNameFunc = originalFinder
	})

	if err := controller.WaitForProcessStart(2 * time.Second); err != nil {
		t.Fatalf("WaitForProcessStart failed: %v", err)
	}
	if findCalls == 0 {
		t.Fatal("expected launcher startup wait to inspect the configured game process name")
	}
}

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func writeTestFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFindProcessesByNameMatchesLinuxLongExecutableBasename(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test covers Linux /proc process-name lookup")
	}

	tempDir, err := os.MkdirTemp("", "gabs_process_name_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	processName := "gabs-stop-fallback"
	processPath := filepath.Join(tempDir, processName)
	if err := os.Symlink("/bin/sleep", processPath); err != nil {
		t.Fatalf("failed to create process-name symlink: %v", err)
	}

	cmd := exec.Command(processPath, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		pids, err := findProcessesByName(processName)
		if err != nil {
			t.Fatalf("findProcessesByName failed: %v", err)
		}
		for _, pid := range pids {
			if pid == cmd.Process.Pid {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %q with pid %d was not found by executable basename; got pids %v", processName, cmd.Process.Pid, pids)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
