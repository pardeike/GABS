package steam

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
)

var (
	startClientCommand  = defaultStartClientCommand
	clientRunning       = defaultClientRunning
	coldStartReadyDelay = 20 * time.Second
	warmStartReadyDelay = 750 * time.Millisecond
)

type App struct {
	AppID         string
	Name          string
	InstallDir    string
	LibraryPath   string
	InstallPath   string
	Executable    string
	WorkingDir    string
	AppIDFilePath string
}

func ResolveApp(appID string) (App, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return App{}, errors.New("Steam app id is required")
	}

	libraries, err := LibraryFolders()
	if err != nil {
		return App{}, err
	}
	if len(libraries) == 0 {
		return App{}, errors.New("no Steam library folders found")
	}

	var checked []string
	for _, library := range libraries {
		steamapps := steamappsPath(library)
		manifestPath := filepath.Join(steamapps, fmt.Sprintf("appmanifest_%s.acf", appID))
		checked = append(checked, manifestPath)
		if _, err := os.Stat(manifestPath); err != nil {
			continue
		}

		app, err := appFromManifest(appID, library, manifestPath)
		if err != nil {
			return App{}, err
		}
		return app, nil
	}

	return App{}, fmt.Errorf("Steam app %s was not found; checked manifests: %s", appID, strings.Join(checked, ", "))
}

func LibraryFolders() ([]string, error) {
	manifestPaths := candidateLibraryFoldersFiles()
	libraries := make([]string, 0, len(manifestPaths))
	seen := make(map[string]bool)
	var readErrors []string

	for _, manifestPath := range manifestPaths {
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				readErrors = append(readErrors, fmt.Sprintf("%s: %v", manifestPath, err))
			}
			continue
		}

		baseLibrary := filepath.Clean(filepath.Dir(filepath.Dir(manifestPath)))
		addLibrary(&libraries, seen, baseLibrary)

		parsed, err := parseVDF(data)
		if err != nil {
			readErrors = append(readErrors, fmt.Sprintf("%s: %v", manifestPath, err))
			continue
		}

		root := parsed
		if nested, ok := nestedMap(parsed, "libraryfolders"); ok {
			root = nested
		}
		for _, value := range root {
			folder, ok := value.(map[string]interface{})
			if !ok {
				continue
			}
			if path, ok := stringValue(folder, "path"); ok {
				addLibrary(&libraries, seen, path)
			}
		}
	}

	if len(libraries) > 0 {
		return libraries, nil
	}
	if len(readErrors) > 0 {
		return nil, fmt.Errorf("failed to read Steam library folders: %s", strings.Join(readErrors, "; "))
	}
	return nil, nil
}

func EnsureClientRunning() error {
	wasRunning := clientRunning()
	cmdName, args, err := startClientCommand()
	if err != nil {
		return err
	}
	cmd := exec.Command(cmdName, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start Steam client: %w", err)
	}
	go func() {
		_ = cmd.Wait()
	}()
	if wasRunning {
		time.Sleep(warmStartReadyDelay)
		return nil
	}
	if err := waitForClientRunning(30 * time.Second); err != nil {
		return err
	}
	time.Sleep(coldStartReadyDelay)
	return nil
}

func EnsureAppIDFile(app App) error {
	if app.AppIDFilePath == "" {
		return nil
	}

	existing, err := os.ReadFile(app.AppIDFilePath)
	if err == nil {
		content := strings.TrimSpace(string(existing))
		if content == app.AppID {
			return nil
		}
		if content != "" {
			return fmt.Errorf("%s already contains Steam app id %q, expected %q", app.AppIDFilePath, content, app.AppID)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read %s: %w", app.AppIDFilePath, err)
	}

	if err := os.WriteFile(app.AppIDFilePath, []byte(app.AppID+"\n"), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", app.AppIDFilePath, err)
	}
	return nil
}

func CheckAppIDFile(app App) (bool, string, error) {
	if app.AppIDFilePath == "" {
		return true, "", nil
	}
	data, err := os.ReadFile(app.AppIDFilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", err
	}
	content := strings.TrimSpace(string(data))
	return content == app.AppID, content, nil
}

func SetClientStartCommandForTesting(fn func() (string, []string, error)) func() {
	previous := startClientCommand
	startClientCommand = fn
	return func() {
		startClientCommand = previous
	}
}

func SetClientControlForTesting(
	start func() (string, []string, error),
	running func() bool,
	coldDelay time.Duration,
	warmDelay time.Duration,
) func() {
	previousStart := startClientCommand
	previousRunning := clientRunning
	previousColdDelay := coldStartReadyDelay
	previousWarmDelay := warmStartReadyDelay
	if start != nil {
		startClientCommand = start
	}
	if running != nil {
		clientRunning = running
	}
	coldStartReadyDelay = coldDelay
	warmStartReadyDelay = warmDelay
	return func() {
		startClientCommand = previousStart
		clientRunning = previousRunning
		coldStartReadyDelay = previousColdDelay
		warmStartReadyDelay = previousWarmDelay
	}
}

func appFromManifest(appID, libraryPath, manifestPath string) (App, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return App{}, fmt.Errorf("failed to read Steam app manifest %s: %w", manifestPath, err)
	}
	parsed, err := parseVDF(data)
	if err != nil {
		return App{}, fmt.Errorf("failed to parse Steam app manifest %s: %w", manifestPath, err)
	}

	root := parsed
	if nested, ok := nestedMap(parsed, "AppState"); ok {
		root = nested
	}

	installDir, ok := stringValue(root, "installdir")
	if !ok || strings.TrimSpace(installDir) == "" {
		return App{}, fmt.Errorf("Steam app manifest %s does not contain installdir", manifestPath)
	}
	name, _ := stringValue(root, "name")

	installPath := filepath.Join(steamappsPath(libraryPath), "common", installDir)
	executable, workingDir, appIDFilePath, err := resolveExecutable(installPath, name, installDir)
	if err != nil {
		return App{}, fmt.Errorf("failed to resolve executable for Steam app %s at %s: %w", appID, installPath, err)
	}

	return App{
		AppID:         appID,
		Name:          name,
		InstallDir:    installDir,
		LibraryPath:   filepath.Clean(libraryPath),
		InstallPath:   installPath,
		Executable:    executable,
		WorkingDir:    workingDir,
		AppIDFilePath: appIDFilePath,
	}, nil
}

func candidateLibraryFoldersFiles() []string {
	if override := os.Getenv("GABS_STEAM_LIBRARYFOLDERS"); strings.TrimSpace(override) != "" {
		parts := filepath.SplitList(override)
		files := make([]string, 0, len(parts))
		for _, part := range parts {
			if strings.TrimSpace(part) != "" {
				files = append(files, part)
			}
		}
		return files
	}

	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		if home == "" {
			return nil
		}
		return []string{
			filepath.Join(home, "Library", "Application Support", "Steam", "steamapps", "libraryfolders.vdf"),
		}
	case "windows":
		candidates := []string{}
		for _, root := range windowsSteamRoots() {
			candidates = append(candidates, filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		}
		for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "Steam", "steamapps", "libraryfolders.vdf"))
			}
		}
		return candidates
	default:
		if home == "" {
			return nil
		}
		return []string{
			filepath.Join(home, ".steam", "steam", "steamapps", "libraryfolders.vdf"),
			filepath.Join(home, ".local", "share", "Steam", "steamapps", "libraryfolders.vdf"),
		}
	}
}

func windowsSteamRoots() []string {
	roots := make([]string, 0, 2)
	seen := make(map[string]bool)
	for _, key := range []string{
		`HKCU\Software\Valve\Steam`,
		`HKLM\Software\Valve\Steam`,
		`HKLM\Software\WOW6432Node\Valve\Steam`,
	} {
		root, err := queryWindowsRegistryValue(key, "SteamPath")
		if err != nil || root == "" {
			root, err = queryWindowsRegistryValue(key, "InstallPath")
		}
		if err != nil || strings.TrimSpace(root) == "" {
			continue
		}
		root = filepath.Clean(root)
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots
}

func queryWindowsRegistryValue(key, name string) (string, error) {
	output, err := exec.Command("reg", "query", key, "/v", name).Output()
	if err != nil {
		return "", err
	}
	return parseWindowsRegistryValue(output, name)
}

func parseWindowsRegistryValue(output []byte, name string) (string, error) {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 || !strings.EqualFold(fields[0], name) {
			continue
		}
		return strings.Join(fields[2:], " "), nil
	}
	return "", fmt.Errorf("registry value %s not found", name)
}

func addLibrary(libraries *[]string, seen map[string]bool, path string) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return
	}
	if filepath.Base(path) == "steamapps" {
		path = filepath.Dir(path)
	}
	if seen[path] {
		return
	}
	seen[path] = true
	*libraries = append(*libraries, path)
}

func steamappsPath(libraryPath string) string {
	libraryPath = filepath.Clean(libraryPath)
	if filepath.Base(libraryPath) == "steamapps" {
		return libraryPath
	}
	return filepath.Join(libraryPath, "steamapps")
}

func resolveExecutable(installPath, appName, installDir string) (string, string, string, error) {
	if runtime.GOOS == "darwin" {
		return resolveMacExecutable(installPath, appName, installDir)
	}
	return resolvePortableExecutable(installPath, appName, installDir)
}

func resolveMacExecutable(installPath, appName, installDir string) (string, string, string, error) {
	apps, err := filepath.Glob(filepath.Join(installPath, "*.app"))
	if err != nil {
		return "", "", "", err
	}
	if len(apps) == 0 {
		return resolvePortableExecutable(installPath, appName, installDir)
	}
	sort.Slice(apps, func(i, j int) bool {
		return scorePath(apps[i], installPath, appName, installDir) > scorePath(apps[j], installPath, appName, installDir)
	})

	appPath := apps[0]
	macOSDir := filepath.Join(appPath, "Contents", "MacOS")
	executableName := readBundleExecutable(filepath.Join(appPath, "Contents", "Info.plist"))
	if executableName != "" {
		executablePath := filepath.Join(macOSDir, executableName)
		if isExecutableFile(executablePath) {
			return executablePath, macOSDir, filepath.Join(macOSDir, "steam_appid.txt"), nil
		}
	}

	entries, err := os.ReadDir(macOSDir)
	if err != nil {
		return "", "", "", err
	}
	candidates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(macOSDir, entry.Name())
		if isExecutableFile(path) {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) == 0 {
		return "", "", "", fmt.Errorf("no executable files found in %s", macOSDir)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return scorePath(candidates[i], installPath, appName, installDir) > scorePath(candidates[j], installPath, appName, installDir)
	})
	return candidates[0], macOSDir, filepath.Join(macOSDir, "steam_appid.txt"), nil
}

func resolvePortableExecutable(installPath, appName, installDir string) (string, string, string, error) {
	candidates := make([]string, 0, 8)
	maxDepth := 2
	err := filepath.WalkDir(installPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path != installPath {
			rel, relErr := filepath.Rel(installPath, path)
			if relErr == nil && strings.Count(rel, string(os.PathSeparator)) > maxDepth {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.IsDir() {
			return nil
		}
		if runtime.GOOS == "windows" {
			if strings.EqualFold(filepath.Ext(path), ".exe") && isLikelyGameExecutable(path) {
				candidates = append(candidates, path)
			}
			return nil
		}
		if isExecutableFile(path) && isLikelyGameExecutable(path) {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return "", "", "", err
	}
	if len(candidates) == 0 {
		return "", "", "", fmt.Errorf("no likely game executable found in %s", installPath)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return scorePath(candidates[i], installPath, appName, installDir) > scorePath(candidates[j], installPath, appName, installDir)
	})
	executable := candidates[0]
	workingDir := filepath.Dir(executable)
	return executable, workingDir, filepath.Join(workingDir, "steam_appid.txt"), nil
}

func readBundleExecutable(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	key := []byte("<key>CFBundleExecutable</key>")
	index := bytes.Index(data, key)
	if index == -1 {
		return ""
	}
	rest := data[index+len(key):]
	startTag := []byte("<string>")
	endTag := []byte("</string>")
	start := bytes.Index(rest, startTag)
	if start == -1 {
		return ""
	}
	rest = rest[start+len(startTag):]
	end := bytes.Index(rest, endTag)
	if end == -1 {
		return ""
	}
	return strings.TrimSpace(string(rest[:end]))
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(path), ".exe")
	}
	return info.Mode()&0111 != 0
}

func isLikelyGameExecutable(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	excluded := []string{
		"crash", "unitycrashhandler", "uninstall", "unins", "redist", "vcredist",
		"dotnet", "dxsetup", "installer", "installscript", "helper",
	}
	for _, token := range excluded {
		if strings.Contains(name, token) {
			return false
		}
	}
	return true
}

func scorePath(path, installPath, appName, installDir string) int {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.TrimSuffix(base, ".app")
	baseNorm := normalizeName(base)
	nameNorm := normalizeName(appName)
	installNorm := normalizeName(installDir)

	score := 0
	switch {
	case baseNorm != "" && baseNorm == nameNorm:
		score += 100
	case baseNorm != "" && baseNorm == installNorm:
		score += 95
	case baseNorm != "" && nameNorm != "" && strings.Contains(nameNorm, baseNorm):
		score += 70
	case baseNorm != "" && nameNorm != "" && strings.Contains(baseNorm, nameNorm):
		score += 65
	case baseNorm != "" && installNorm != "" && strings.Contains(baseNorm, installNorm):
		score += 60
	}

	if rel, err := filepath.Rel(installPath, path); err == nil {
		score -= strings.Count(rel, string(os.PathSeparator)) * 5
	}
	if strings.Contains(strings.ToLower(filepath.Base(path)), "launcher") {
		score -= 20
	}
	return score
}

func normalizeName(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func defaultStartClientCommand() (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{"-a", "Steam"}, nil
	case "windows":
		return "cmd", []string{"/c", "start", "", "steam://open/main"}, nil
	default:
		return "steam", []string{"-silent"}, nil
	}
}

func waitForClientRunning(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if clientRunning() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Steam client did not become visible within %s", timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func defaultClientRunning() bool {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("pgrep", "-f", "/Steam/Contents/MacOS/steam_osx").Run() == nil
	case "windows":
		output, err := exec.Command("tasklist", "/FI", "IMAGENAME eq steam.exe").Output()
		if err != nil {
			return false
		}
		return strings.Contains(strings.ToLower(string(output)), "steam.exe")
	default:
		return exec.Command("pgrep", "-x", "steam").Run() == nil ||
			exec.Command("pgrep", "-f", "/steam").Run() == nil
	}
}
