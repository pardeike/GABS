package mcp

import (
	"os"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/process"
)

func TestMain(m *testing.M) {
	restoreLaunchers := process.SetLaunchCommandFactoriesForTesting(
		testLauncherCommandFactory("steam"),
		testLauncherCommandFactory("epic"),
	)
	code := m.Run()
	restoreLaunchers()
	os.Exit(code)
}

func testLauncherCommandFactory(kind string) func(string) (string, []string) {
	return func(target string) (string, []string) {
		return os.Args[0], []string{
			"-test.run=TestLauncherHelperProcess",
			"--",
			kind,
			target,
		}
	}
}

func TestLauncherHelperProcess(t *testing.T) {
	args := os.Args
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}

	if separator == -1 {
		return
	}

	if len(args) < separator+3 {
		t.Fatalf("launcher helper received incomplete args: %v", args)
	}

	// Keep the helper process alive long enough for launcher-state polling,
	// then exit successfully without invoking any external launcher.
	time.Sleep(750 * time.Millisecond)
}
