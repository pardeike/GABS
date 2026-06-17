package steam

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseVDFLibraryFolders(t *testing.T) {
	parsed, err := parseVDF([]byte(`
		"libraryfolders"
		{
			"0"
			{
				"path" "/Users/ap/Library/Application Support/Steam"
				"apps"
				{
					"123456" "123"
				}
			}
		}
	`))
	if err != nil {
		t.Fatalf("parseVDF failed: %v", err)
	}
	root, ok := nestedMap(parsed, "libraryfolders")
	if !ok {
		t.Fatalf("expected libraryfolders root: %#v", parsed)
	}
	folder, ok := nestedMap(root, "0")
	if !ok {
		t.Fatalf("expected folder 0: %#v", root)
	}
	path, ok := stringValue(folder, "path")
	if !ok || path != "/Users/ap/Library/Application Support/Steam" {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestParseVDFUnescapesWindowsLibraryPath(t *testing.T) {
	parsed, err := parseVDF([]byte(`
		"libraryfolders"
		{
			"0"
			{
				"path" "D:\\SteamLibrary"
			}
		}
	`))
	if err != nil {
		t.Fatalf("parseVDF failed: %v", err)
	}
	root, ok := nestedMap(parsed, "libraryfolders")
	if !ok {
		t.Fatalf("expected libraryfolders root: %#v", parsed)
	}
	folder, ok := nestedMap(root, "0")
	if !ok {
		t.Fatalf("expected folder 0: %#v", root)
	}
	path, ok := stringValue(folder, "path")
	if !ok || path != `D:\SteamLibrary` {
		t.Fatalf("unexpected path %q", path)
	}
}

func TestParseWindowsRegistryValue(t *testing.T) {
	output := []byte(`
HKEY_CURRENT_USER\Software\Valve\Steam
    SteamPath    REG_SZ    C:\Program Files (x86)\Steam
`)

	value, err := parseWindowsRegistryValue(output, "SteamPath")
	if err != nil {
		t.Fatalf("parseWindowsRegistryValue failed: %v", err)
	}
	if value != `C:\Program Files (x86)\Steam` {
		t.Fatalf("unexpected registry value %q", value)
	}
}

func TestResolveAppFromManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test creates Unix executable permissions")
	}

	tempDir := t.TempDir()
	library := filepath.Join(tempDir, "Steam")
	steamapps := filepath.Join(library, "steamapps")
	install := filepath.Join(steamapps, "common", "ExampleGame")
	exe := filepath.Join(install, "ExampleGame")

	mustWrite(t, filepath.Join(steamapps, "libraryfolders.vdf"), `
		"libraryfolders"
		{
			"0" { "path" "`+library+`" }
		}
	`)
	mustWrite(t, filepath.Join(steamapps, "appmanifest_123456.acf"), `
		"AppState"
		{
			"appid" "123456"
			"name" "Example Game"
			"installdir" "ExampleGame"
		}
	`)
	if err := os.MkdirAll(install, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GABS_STEAM_LIBRARYFOLDERS", filepath.Join(steamapps, "libraryfolders.vdf"))

	app, err := ResolveApp("123456")
	if err != nil {
		t.Fatalf("ResolveApp failed: %v", err)
	}
	if app.AppID != "123456" {
		t.Fatalf("unexpected app id %q", app.AppID)
	}
	if app.Executable != exe {
		t.Fatalf("expected executable %s, got %s", exe, app.Executable)
	}
	if app.WorkingDir != install {
		t.Fatalf("expected working dir %s, got %s", install, app.WorkingDir)
	}
}

func TestResolveMacAppBundleUsesInfoPlistExecutable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS app bundle resolution")
	}

	tempDir := t.TempDir()
	library := filepath.Join(tempDir, "Steam")
	steamapps := filepath.Join(library, "steamapps")
	install := filepath.Join(steamapps, "common", "ExampleGame")
	appBundle := filepath.Join(install, "ExampleGameMac.app")
	macOSDir := filepath.Join(appBundle, "Contents", "MacOS")
	exe := filepath.Join(macOSDir, "Example Game")

	mustWrite(t, filepath.Join(steamapps, "libraryfolders.vdf"), `
		"libraryfolders"
		{
			"0" { "path" "`+library+`" }
		}
	`)
	mustWrite(t, filepath.Join(steamapps, "appmanifest_123456.acf"), `
		"AppState"
		{
			"appid" "123456"
			"name" "Example Game"
			"installdir" "ExampleGame"
		}
	`)
	mustWrite(t, filepath.Join(appBundle, "Contents", "Info.plist"), `
		<plist>
		<dict>
			<key>CFBundleExecutable</key>
			<string>Example Game</string>
		</dict>
		</plist>
	`)
	if err := os.MkdirAll(macOSDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GABS_STEAM_LIBRARYFOLDERS", filepath.Join(steamapps, "libraryfolders.vdf"))

	app, err := ResolveApp("123456")
	if err != nil {
		t.Fatalf("ResolveApp failed: %v", err)
	}
	if app.Executable != exe {
		t.Fatalf("expected executable %s, got %s", exe, app.Executable)
	}
	if app.WorkingDir != macOSDir {
		t.Fatalf("expected working dir %s, got %s", macOSDir, app.WorkingDir)
	}
	if !strings.HasSuffix(app.AppIDFilePath, filepath.Join("Contents", "MacOS", "steam_appid.txt")) {
		t.Fatalf("unexpected app id file path %s", app.AppIDFilePath)
	}
}

func TestEnsureAppIDFileCreatesMissingFile(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "steam_appid.txt")
	app := App{AppID: "123456", AppIDFilePath: path}

	if err := EnsureAppIDFile(app); err != nil {
		t.Fatalf("EnsureAppIDFile failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "123456" {
		t.Fatalf("unexpected app id file content %q", string(data))
	}
}

func TestEnsureAppIDFileRejectsDifferentID(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "steam_appid.txt")
	if err := os.WriteFile(path, []byte("111\n"), 0644); err != nil {
		t.Fatal(err)
	}
	app := App{AppID: "123456", AppIDFilePath: path}

	err := EnsureAppIDFile(app)
	if err == nil {
		t.Fatal("expected error for conflicting app id file")
	}
	if !strings.Contains(err.Error(), "expected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureClientRunningWaitsForColdStartVisibility(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses current test binary as helper command")
	}

	runningChecks := 0
	restore := SetClientControlForTesting(
		func() (string, []string, error) {
			return os.Args[0], []string{"-test.run=TestSteamClientHelper", "--"}, nil
		},
		func() bool {
			runningChecks++
			return runningChecks > 1
		},
		0,
		0,
	)
	t.Cleanup(restore)

	if err := EnsureClientRunning(); err != nil {
		t.Fatalf("EnsureClientRunning failed: %v", err)
	}
	if runningChecks < 2 {
		t.Fatalf("expected cold start path to wait for client visibility, got %d checks", runningChecks)
	}
}

func TestSteamClientHelper(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "--" {
			return
		}
	}
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
