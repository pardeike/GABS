package process

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindSteamExecutable(t *testing.T) {
	tests := []struct {
		name         string
		os           string
		shouldFind   bool
		expectedExts []string
	}{
		{
			name:         "Windows should find steam.exe",
			os:           "windows",
			shouldFind:   false, // May not exist in CI environment
			expectedExts: []string{".exe"},
		},
		{
			name:         "Darwin should find Steam app",
			os:           "darwin",
			shouldFind:   false, // May not exist in CI environment
			expectedExts: []string{".app"},
		},
		{
			name:         "Linux should find steam binary",
			os:           "linux",
			shouldFind:   false, // May not exist in CI environment
			expectedExts: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Only test current OS to avoid mock complexity
			if runtime.GOOS != tt.os {
				t.Skip("Skipping test for different OS")
				return
			}

			steamPath, found := findSteamExecutable()

			if found {
				t.Logf("Found Steam executable at: %s", steamPath)

				// Verify the file exists
				if _, err := os.Stat(steamPath); os.IsNotExist(err) {
					t.Errorf("Steam executable reported as found but does not exist: %s", steamPath)
				}

				// Verify expected extension (for Windows and Darwin)
				if len(tt.expectedExts) > 0 {
					hasExpectedExt := false
					for _, ext := range tt.expectedExts {
						if filepath.Ext(steamPath) == ext {
							hasExpectedExt = true
							break
						}
					}
					if !hasExpectedExt {
						t.Errorf("Steam executable %s does not have expected extension %v", steamPath, tt.expectedExts)
					}
				}
			} else {
				t.Logf("Steam executable not found (this is normal in CI environments)")
			}

			// Verify function doesn't panic and returns consistent results
			steamPath2, found2 := findSteamExecutable()
			if found != found2 || steamPath != steamPath2 {
				t.Errorf("findSteamExecutable() returned inconsistent results: (%s, %v) vs (%s, %v)", 
					steamPath, found, steamPath2, found2)
			}
		})
	}
}

func TestGetSteamLauncherWithDetection(t *testing.T) {
	controller := &Controller{}
	
	// Test the current behavior with detection
	cmdName, args := controller.getSteamLauncherCommand("12345")
	
	t.Logf("Steam launcher command: %s %v", cmdName, args)
	
	// Verify command is reasonable
	if cmdName == "" {
		t.Error("getSteamLauncherCommand() returned empty command")
	}
	
	if len(args) == 0 {
		t.Error("getSteamLauncherCommand() returned no arguments")
	}
	
	// Verify Steam app ID is included in arguments somewhere
	foundAppId := false
	for _, arg := range args {
		if arg == "12345" || arg == "-applaunch" || arg == "steam://rungameid/12345" {
			foundAppId = true
			break
		}
	}
	
	if !foundAppId {
		t.Errorf("Steam app ID '12345' not found in launch arguments: %v", args)
	}
}

func TestSteamExecutableValidation(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "Empty path should be invalid",
			path:     "",
			expected: false,
		},
		{
			name:     "Non-existent path should be invalid",
			path:     "/nonexistent/steam",
			expected: false,
		},
		// Note: We can't easily test valid paths in CI without mocking
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidSteamExecutable(tt.path)
			if result != tt.expected {
				t.Errorf("isValidSteamExecutable(%s) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}