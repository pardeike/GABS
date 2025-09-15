// cmd/gabs/main.go
// Go 1.22+
//
// GABS - Game Agent Bridge Server
// Simplified architecture: Configuration-first approach with MCP-native game management

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/util"
	"github.com/pardeike/gabs/internal/version"
)



const defaultBackoff = "100ms..5s"

type options struct {
	subcmd string

	// Server transport
	httpAddr string // if empty → stdio

	// Config + runtime
	configDir  string
	logLevel   string
	backoffStr string
	backoffMin time.Duration
	backoffMax time.Duration

	// Policy
	graceStop time.Duration
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	subcmd := os.Args[1]
	if subcmd == "-h" || subcmd == "--help" || subcmd == "help" {
		usage()
		return
	}

	fs := flag.NewFlagSet(subcmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		httpAddr  = fs.String("http", "", "Run MCP as HTTP on addr (default stdio if empty)")
		configDir = fs.String("configDir", "", "Override GABS config directory")
		logLevel  = fs.String("log-level", "info", "Log level: trace|debug|info|warn|error")
		backoff   = fs.String("reconnectBackoff", defaultBackoff, "Reconnect backoff window, e.g. '100ms..5s'")
		grace     = fs.Duration("grace", 3*time.Second, "Graceful stop timeout before kill")
	)

	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	min, max, err := parseBackoff(*backoff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --reconnectBackoff: %v\n", err)
		os.Exit(2)
	}

	opts := options{
		subcmd:     subcmd,
		httpAddr:   *httpAddr,
		configDir:  *configDir,
		logLevel:   *logLevel,
		backoffStr: *backoff,
		backoffMin: min,
		backoffMax: max,
		graceStop:  *grace,
	}

	// Initialize structured logger to stderr only
	log := util.NewLogger(opts.logLevel)

	// Suppress startup log for "games" and "version" commands to keep output clean for terminal usage
	if subcmd != "games" && subcmd != "version" {
		log.Infow("starting gabs", "version", version.Get(), "commit", version.GetCommit(), "built", version.GetBuildDate(), "subcmd", subcmd)
	}

	// Context with OS signals
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var exitCode int
	switch subcmd {
	case "server":
		exitCode = runServer(ctx, log, opts)
	case "games":
		exitCode = manageGames(ctx, log, opts, fs.Args())
	case "version":
		fmt.Printf("%s %s (%s)\n", "gabs", version.Get(), version.GetCommit())
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcmd)
		usage()
		os.Exit(2)
	}

	os.Exit(exitCode)
}

func usage() {
	fmt.Fprintf(os.Stderr, `gabs %s

GABS - Game Agent Bridge Server
Configuration-first approach with MCP-native game management

Usage:
  gabs <subcommand> [flags]

Subcommands:
  server     Start the GABS MCP server
  games      Manage game configurations
  version    Print version information

Server flags:
  --http <addr>                 Run MCP as HTTP on address; if empty, use stdio
  --configDir <dir>             Override GABS config directory  
  --reconnectBackoff <min..max> Reconnect backoff window (default %s)
  --log-level <lvl>             trace|debug|info|warn|error
  --grace <dur>                 Graceful stop timeout (default 3s)

Game management:
  gabs games list               List configured game IDs (simplified output)
  gabs games add <id>           Add a new game configuration (interactive)
  gabs games remove <id>        Remove a game configuration
  gabs games show <id>          Show details for a game

Examples:
  # Start GABS MCP server (stdio)
  gabs server
  
  # Start GABS MCP server (HTTP)  
  gabs server --http localhost:8080
  
  # Add a new game configuration
  gabs games add minecraft
  
  # List configured games (shows only game IDs)
  gabs games list

Once the server is running, use MCP tools to manage games:
  games.list        List configured game IDs (simplified for AI)
  games.status      Check status of specific games
  games.start       Start a game
  games.stop        Gracefully stop a game  
  games.kill        Force terminate a game
`, version.Get(), defaultBackoff)
}

// === Server Command ===

func runServer(ctx context.Context, log util.Logger, opts options) int {
	// Load games configuration
	gamesConfig, err := config.LoadGamesConfigFromDir(opts.configDir)
	if err != nil {
		log.Errorw("failed to load games config", "error", err)
		return 1
	}

	log.Infow("loaded games configuration", "gameCount", len(gamesConfig.Games))

	// Create MCP server with game management tools
	server := mcp.NewServer(log)
	server.SetConfigDir(opts.configDir)

	// Register game management tools
	server.RegisterGameManagementTools(gamesConfig, opts.backoffMin, opts.backoffMax)

	// Start serving MCP according to transport
	errCh := make(chan error, 1)
	go func() {
		if opts.httpAddr == "" {
			log.Infow("starting MCP server", "transport", "stdio")
			errCh <- server.ServeStdio(ctx)
		} else {
			log.Infow("starting MCP server", "transport", "http", "addr", opts.httpAddr)
			errCh <- server.ServeHTTP(ctx, opts.httpAddr)
		}
	}()

	select {
	case <-ctx.Done():
		log.Infow("shutdown signal received")
		return 0
	case err := <-errCh:
		if err != nil {
			log.Errorw("server exited with error", "error", err)
			return 1
		}
		return 0
	}
}

// === Games Configuration Management ===

func manageGames(ctx context.Context, log util.Logger, opts options, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Please specify what you'd like to do with games.\n\n")
		showGamesUsage()
		return 2
	}

	action := args[0]

	switch action {
	case "list":
		return listGames(log, opts.configDir)
	case "add":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "games add requires a game ID\n")
			return 2
		}
		return addGame(log, args[1], opts.configDir)
	case "remove":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "games remove requires a game ID\n")
			return 2
		}
		return removeGame(log, args[1], opts.configDir)
	case "show":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "games show requires a game ID\n")
			return 2
		}
		return showGame(log, args[1], opts.configDir)
	default:
		fmt.Fprintf(os.Stderr, "unknown games action: %s\n", action)
		return 2
	}
}

func listGames(log util.Logger, configDir string) int {
	gamesConfig, err := config.LoadGamesConfigFromDir(configDir)
	if err != nil {
		log.Errorw("failed to load games config", "error", err)
		return 1
	}

	games := gamesConfig.ListGames()
	if len(games) == 0 {
		fmt.Println("No games configured. Use 'gabs games add <id>' to add games.")
		return 0
	}

	for _, game := range games {
		fmt.Println(game.ID)
	}
	return 0
}

func addGame(log util.Logger, gameID string, configDir string) int {
	gamesConfig, err := config.LoadGamesConfigFromDir(configDir)
	if err != nil {
		log.Errorw("failed to load games config", "error", err)
		return 1
	}

	// Check if game already exists
	if _, exists := gamesConfig.GetGame(gameID); exists {
		fmt.Printf("Game '%s' already exists. Use 'gabs games show %s' to view it.\n", gameID, gameID)
		return 1
	}

	// For automated environments, provide a minimal config
	if !isInteractive() {
		game := config.GameConfig{
			ID:         gameID,
			Name:       gameID,
			LaunchMode: "DirectPath",
			Target:     "",
		}
		if err := gamesConfig.AddGame(game); err != nil {
			log.Errorw("invalid game configuration", "error", err)
			return 1
		}
		
		if err := config.SaveGamesConfigToDir(gamesConfig, configDir); err != nil {
			log.Errorw("failed to save games config", "error", err)
			return 1
		}

		fmt.Printf("Game '%s' added with minimal configuration. Configure it manually or edit the config file.\n", gameID)
		return 0
	}

	// Interactive game configuration
	fmt.Printf("Adding game configuration for '%s':\n", gameID)
	game := config.GameConfig{
		ID:         gameID,
		Name:       promptString("Game Name", gameID),
		LaunchMode: promptChoice("Launch Mode", "DirectPath", []string{"DirectPath", "SteamAppId", "EpicAppId", "CustomCommand"}),
	}

	// Enhance target prompt for DirectPath mode with platform-specific help
	var targetPrompt string
	if game.LaunchMode == "DirectPath" {
		if runtime.GOOS == "darwin" {
			targetPrompt = "Target (executable path or .app bundle)"
		} else {
			targetPrompt = "Target (executable path)"
		}
	} else {
		targetPrompt = "Target (path/id)"
	}

	game.Target = promptString(targetPrompt, "")

	// For DirectPath on macOS, resolve .app bundles to actual executables
	if game.LaunchMode == "DirectPath" && game.Target != "" {
		if resolvedTarget, err := resolveMacOSAppBundle(game.Target); err == nil && resolvedTarget != game.Target {
			fmt.Printf("✓ Resolved app bundle to executable: %s\n", resolvedTarget)
			game.Target = resolvedTarget
		}
	}

	if game.LaunchMode == "DirectPath" || game.LaunchMode == "CustomCommand" {
		workingDir := promptString("Working Directory (optional)", "")
		if workingDir != "" {
			game.WorkingDir = workingDir
		}
	}

	// Ask for optional stop process name for better game termination control
	// For launcher-based games (Steam/Epic), this is required
	var stopProcessName string
	if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
		stopProcessName = promptString(fmt.Sprintf("Stop Process Name (REQUIRED for %s games)", game.LaunchMode), "")
		for stopProcessName == "" {
			fmt.Printf("⚠️  Stop Process Name is required for %s games to enable proper game termination.\n", game.LaunchMode)
			fmt.Printf("   Without it, GABS can only stop the launcher process, not the actual game.\n")
			fmt.Printf("   Examples: 'RimWorldWin64.exe' for RimWorld, 'java' for Minecraft\n")
			stopProcessName = promptString(fmt.Sprintf("Stop Process Name (REQUIRED for %s games)", game.LaunchMode), "")
		}
	} else {
		stopProcessName = promptString("Stop Process Name (optional - for better game stopping)", "")
	}
	if stopProcessName != "" {
		game.StopProcessName = stopProcessName
	}

	description := promptString("Description (optional)", "")
	if description != "" {
		game.Description = description
	}

	if err := gamesConfig.AddGame(game); err != nil {
		log.Errorw("invalid game configuration", "error", err)
		return 1
	}
	
	if err := config.SaveGamesConfigToDir(gamesConfig, configDir); err != nil {
		log.Errorw("failed to save games config", "error", err)
		return 1
	}

	fmt.Printf("Game '%s' added successfully.\n", gameID)
	return 0
}

func removeGame(log util.Logger, gameID string, configDir string) int {
	gamesConfig, err := config.LoadGamesConfigFromDir(configDir)
	if err != nil {
		log.Errorw("failed to load games config", "error", err)
		return 1
	}

	if !gamesConfig.RemoveGame(gameID) {
		fmt.Printf("Game '%s' not found.\n", gameID)
		return 1
	}

	if err := config.SaveGamesConfigToDir(gamesConfig, configDir); err != nil {
		log.Errorw("failed to save games config", "error", err)
		return 1
	}

	fmt.Printf("Game '%s' removed successfully.\n", gameID)
	return 0
}

func showGame(log util.Logger, gameID string, configDir string) int {
	gamesConfig, err := config.LoadGamesConfigFromDir(configDir)
	if err != nil {
		log.Errorw("failed to load games config", "error", err)
		return 1
	}

	game, exists := gamesConfig.GetGame(gameID)
	if !exists {
		fmt.Printf("Game '%s' not found.\n", gameID)
		return 1
	}

	fmt.Printf("Game Configuration: %s\n", game.ID)
	fmt.Printf("  Name: %s\n", game.Name)
	fmt.Printf("  Launch Mode: %s\n", game.LaunchMode)
	fmt.Printf("  Target: %s\n", game.Target)
	if game.WorkingDir != "" {
		fmt.Printf("  Working Directory: %s\n", game.WorkingDir)
	}
	if len(game.Args) > 0 {
		fmt.Printf("  Arguments: %s\n", strings.Join(game.Args, " "))
	}
	if game.StopProcessName != "" {
		fmt.Printf("  Stop Process Name: %s\n", game.StopProcessName)
	}
	if game.Description != "" {
		fmt.Printf("  Description: %s\n", game.Description)
	}

	return 0
}

// === Helper Functions ===

func showGamesUsage() {
	fmt.Fprintf(os.Stderr, `Game Management Commands:
  gabs games list               List configured game IDs (simplified output)
  gabs games add <id>           Add a new game configuration (interactive)
  gabs games remove <id>        Remove a game configuration
  gabs games show <id>          Show details for a game

Examples:
  gabs games list               # See game IDs only (AI-friendly)
  gabs games add minecraft      # Add a new game called 'minecraft'
  gabs games show minecraft     # View configuration for 'minecraft'
  gabs games remove minecraft   # Remove the 'minecraft' configuration
`)
}

// isInteractive checks if the program is running in an interactive terminal
func isInteractive() bool {
	// Check if stdin is a terminal
	fileInfo, _ := os.Stdin.Stat()
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

func promptString(prompt, defaultValue string) string {
	if defaultValue != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Printf("%s: ", prompt)
	}

	// Use bufio.Scanner to read the entire line, including spaces
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			return defaultValue
		}
		return input
	}

	// If scan failed or reached EOF, return default value
	return defaultValue
}

func promptChoice(prompt, defaultValue string, choices []string) string {
	fmt.Printf("%s ", prompt)
	if len(choices) > 0 {
		fmt.Printf("(%s)", strings.Join(choices, "|"))
	}
	if defaultValue != "" {
		fmt.Printf(" [%s]", defaultValue)
	}
	fmt.Print(": ")

	// Use bufio.Scanner to read the entire line, including spaces
	scanner := bufio.NewScanner(os.Stdin)
	var input string
	if scanner.Scan() {
		input = strings.TrimSpace(scanner.Text())
	}

	if input == "" {
		return defaultValue
	}

	// Validate choice
	for _, choice := range choices {
		if input == choice {
			return input
		}
	}

	fmt.Printf("Invalid choice. Please select one of: %s\n", strings.Join(choices, ", "))
	return promptChoice(prompt, defaultValue, choices)
}

// resolveMacOSAppBundle resolves a macOS .app bundle path to the actual executable inside it
func resolveMacOSAppBundle(appPath string) (string, error) {
	// Only process on macOS and only for .app bundles
	if runtime.GOOS != "darwin" || !strings.HasSuffix(appPath, ".app") {
		return appPath, nil
	}

	// Check if the app bundle exists
	if _, err := os.Stat(appPath); os.IsNotExist(err) {
		return appPath, nil // Return original path if it doesn't exist (user might be entering a path that doesn't exist yet)
	}

	// Look for executables in Contents/MacOS/
	macOSDir := filepath.Join(appPath, "Contents", "MacOS")
	if _, err := os.Stat(macOSDir); os.IsNotExist(err) {
		// Not a standard app bundle structure, but might be valid - warn user
		fmt.Printf("⚠️  Warning: %s doesn't appear to be a standard app bundle (missing Contents/MacOS)\n", filepath.Base(appPath))
		return appPath, nil
	}

	entries, err := os.ReadDir(macOSDir)
	if err != nil {
		fmt.Printf("⚠️  Warning: Cannot read Contents/MacOS directory in %s\n", filepath.Base(appPath))
		return appPath, nil
	}

	var executables []string
	appName := strings.TrimSuffix(filepath.Base(appPath), ".app")

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// Check if file is executable
		fullPath := filepath.Join(macOSDir, entry.Name())
		if info, err := os.Stat(fullPath); err == nil {
			if info.Mode()&0111 != 0 { // Has execute permission
				executables = append(executables, entry.Name())
			}
		}
	}

	if len(executables) == 0 {
		fmt.Printf("⚠️  Warning: No executable files found in %s/Contents/MacOS\n", filepath.Base(appPath))
		return appPath, nil
	}

	// If there's only one executable, use it
	if len(executables) == 1 {
		fmt.Printf("🔍 Found executable: %s\n", executables[0])
		return filepath.Join(macOSDir, executables[0]), nil
	}

	// Multiple executables - try to find one that matches the app name
	for _, executable := range executables {
		if strings.Contains(strings.ToLower(executable), strings.ToLower(appName)) {
			fmt.Printf("🔍 Found matching executable: %s\n", executable)
			return filepath.Join(macOSDir, executable), nil
		}
	}

	// Multiple executables, none match app name - let user choose
	fmt.Printf("\n🔍 Found multiple executables in %s:\n", filepath.Base(appPath))
	for i, executable := range executables {
		fmt.Printf("  %d. %s\n", i+1, executable)
	}

	for {
		choice := promptString("Select executable (1-"+fmt.Sprintf("%d", len(executables))+")", "1")
		if choice == "" {
			choice = "1"
		}

		// Parse choice
		var index int
		if _, err := fmt.Sscanf(choice, "%d", &index); err == nil && index >= 1 && index <= len(executables) {
			selectedExecutable := executables[index-1]
			fmt.Printf("✓ Selected: %s\n", selectedExecutable)
			return filepath.Join(macOSDir, selectedExecutable), nil
		}

		fmt.Printf("Please enter a number between 1 and %d\n", len(executables))
	}
}

func parseBackoff(s string) (time.Duration, time.Duration, error) {
	// Parse "<min>..<max>" format
	// Examples: "100ms..5s", "1s..30s", "250ms..inf"
	switch {
	case s == "", s == defaultBackoff:
		return 100 * time.Millisecond, 5 * time.Second, nil
	default:
		// Split on ".."
		parts := strings.Split(s, "..")
		if len(parts) != 2 {
			return 100 * time.Millisecond, 5 * time.Second, fmt.Errorf("invalid format, expected 'min..max'")
		}

		min, err := time.ParseDuration(parts[0])
		if err != nil {
			return 100 * time.Millisecond, 5 * time.Second, fmt.Errorf("invalid min duration: %w", err)
		}
		if min < 0 {
			return 100 * time.Millisecond, 5 * time.Second, fmt.Errorf("min duration cannot be negative")
		}

		var max time.Duration
		if parts[1] == "inf" {
			max = time.Hour * 24 // Large duration for "infinite"
		} else {
			max, err = time.ParseDuration(parts[1])
			if err != nil {
				return 100 * time.Millisecond, 5 * time.Second, fmt.Errorf("invalid max duration: %w", err)
			}
			if max < 0 {
				return 100 * time.Millisecond, 5 * time.Second, fmt.Errorf("max duration cannot be negative")
			}
		}

		if min > max {
			return 100 * time.Millisecond, 5 * time.Second, fmt.Errorf("min duration (%v) cannot be greater than max duration (%v)", min, max)
		}

		return min, max, nil
	}
}
