// cmd/gabs/main.go
// Go 1.22+
//
// PROMPT: Set module path to your repo. Example: module github.com/pardeike/gabs
// PROMPT: This file wires CLI → process controller → GABP client → MCP server.
// PROMPT: Leave all TODOs for codegen; keep real logic minimal here.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	// PROMPT: Replace with your module path.
	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/mcp"
	"github.com/pardeike/gabs/internal/mirror"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

var (
	Version   = "0.1.0"
	BuildDate = "unknown"
	Commit    = "unknown"
)

const defaultBackoff = "100ms..5s"

type options struct {
	subcmd     string
	gameID     string
	launchMode string // DirectPath|SteamAppId|EpicAppId|CustomCommand
	target     string // path or id
	args       []string
	cwd        string

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

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, " ") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
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
		gameID     = fs.String("gameId", "", "Game/application identifier (e.g. 'rimworld')")
		launchMode = fs.String("launch", "DirectPath", "Launch mode: DirectPath|SteamAppId|EpicAppId|CustomCommand")
		target     = fs.String("target", "", "Path or ID depending on launch mode")
		cwd        = fs.String("cwd", "", "Working directory for DirectPath/CustomCommand")
		httpAddr   = fs.String("http", "", "Run MCP as Streamable HTTP on addr (default stdio if empty)")
		configDir  = fs.String("configDir", "", "Override GAB config directory")
		logLevel   = fs.String("log-level", "info", "Log level: trace|debug|info|warn|error")
		backoff    = fs.String("reconnectBackoff", defaultBackoff, "Reconnect backoff window, e.g. '100ms..5s'")
		grace      = fs.Duration("grace", 3*time.Second, "Graceful stop timeout before kill")
	)

	var argv multiFlag
	fs.Var(&argv, "arg", "Repeatable arg for DirectPath/CustomCommand (can be specified multiple times)")

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
		gameID:     *gameID,
		launchMode: *launchMode,
		target:     *target,
		args:       argv,
		cwd:        *cwd,
		httpAddr:   *httpAddr,
		configDir:  *configDir,
		logLevel:   *logLevel,
		backoffStr: *backoff,
		backoffMin: min,
		backoffMax: max,
		graceStop:  *grace,
	}

	// PROMPT: Initialize structured logger to stderr only; do not write to stdout if using stdio MCP.
	log := util.NewLogger(opts.logLevel)
	log.Infow("starting gabs", "version", Version, "commit", Commit, "built", BuildDate, "subcmd", subcmd)

	// Context with OS signals
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var exitCode int
	switch subcmd {
	case "run":
		exitCode = run(ctx, log, opts)
	case "start":
		exitCode = cmdStart(ctx, log, opts)
	case "stop":
		exitCode = cmdStop(ctx, log, opts)
	case "kill":
		exitCode = cmdKill(ctx, log, opts)
	case "restart":
		exitCode = cmdRestart(ctx, log, opts)
	case "attach":
		exitCode = cmdAttach(ctx, log, opts)
	case "status":
		exitCode = cmdStatus(ctx, log, opts)
	case "version":
		fmt.Printf("%s %s (%s)\n", "gabs", Version, Commit)
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

Usage:
  gabs <subcommand> [flags]

Subcommands:
  run        Start MCP server, launch/attach app, mirror GABP tools/resources/events
  start      Launch target app only and write bridge.json
  stop       Gracefully stop target app
  kill       Force terminate target app
  restart    Restart target app
  attach     Attach to already running app and run MCP server
  status     Print target app status
  version    Print version

Common flags (run/start/attach):
  --gameId <id>                 Game/application identifier (e.g. rimworld)
  --launch <mode>               DirectPath|SteamAppId|EpicAppId|CustomCommand (default DirectPath)
  --target <pathOrId>           Path or ID depending on launch mode
  --arg <value>                 Repeatable, forwarded to target (DirectPath/CustomCommand)
  --cwd <dir>                   Working dir for DirectPath/CustomCommand
  --http <addr>                 Run MCP as HTTP on address; if empty, use stdio
  --configDir <dir>             Override config dir for bridge.json
  --reconnectBackoff <min..max> Reconnect backoff window (default `+defaultBackoff+`)
  --log-level <lvl>             trace|debug|info|warn|error
  --grace <dur>                 Graceful stop timeout for stop/restart (default 3s)

Examples:
  gabs run --gameId rimworld --launch SteamAppId --target 294100
  gabs run --gameId rimworld --launch DirectPath --target "/Applications/RimWorld.app" --arg "-logfile" --arg "-"
`)
}

// === Subcommands ===

func run(ctx context.Context, log util.Logger, opts options) int {
	// PROMPT: Validate inputs; ensure gameId present. For Steam/Epic, target must be set.
	if opts.gameID == "" {
		fmt.Fprintln(os.Stderr, "--gameId is required")
		return 2
	}

	// PROMPT: Compute config path and write bridge.json atomically. Generate random port+token.
	port, token, cfgPath, err := config.WriteBridgeJSON(opts.gameID, opts.configDir)
	if err != nil {
		log.Errorw("failed to write bridge.json", "error", err)
		return 1
	}
	log.Debugw("bridge.json prepared", "path", cfgPath, "port", port)

	// PROMPT: Configure and start target application according to launch mode.
	ctrl := &process.Controller{}
	if err := ctrl.Configure(process.LaunchSpec{
		GameId:     opts.gameID,
		Mode:       opts.launchMode,
		PathOrId:   opts.target,
		Args:       opts.args,
		WorkingDir: opts.cwd,
	}); err != nil {
		log.Errorw("failed to configure app", "error", err)
		return 1
	}
	if err := ctrl.Start(); err != nil {
		log.Errorw("failed to start app", "error", err)
		return 1
	}
	defer func() {
		// PROMPT: Stop or leave running based on policy/env; default to keep running on HTTP, stop on stdio exit.
		_ = ctrl
	}()

	// PROMPT: Connect to GABP server with reconnect/backoff and hello/welcome handshake.
	client := gabp.NewClient(log)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := client.Connect(addr, token, opts.backoffMin, opts.backoffMax); err != nil {
		log.Errorw("failed to connect to GABP", "addr", addr, "error", err)
		return 1
	}

	// PROMPT: Create MCP server (stdio or HTTP). Register bridge.* tools (start/stop/kill/restart, attach, tools.refresh).
	server := mcp.NewServer(log)

	// PROMPT: Build mirror that maps GABP tools/resources/events into MCP surface. Support hot-refresh via tools.changed event.
	m := mirror.New(log, server, client)
	if err := m.SyncTools(); err != nil {
		log.Errorw("tool sync failed", "error", err)
		return 1
	}
	if err := m.ExposeResources(); err != nil {
		log.Errorw("resource expose failed", "error", err)
		return 1
	}
	server.RegisterBridgeTools(ctrl, client)

	// PROMPT: Start serving MCP according to transport. For stdio, ensure no non-protocol writes to stdout.
	errCh := make(chan error, 1)
	go func() {
		if opts.httpAddr == "" {
			errCh <- server.ServeStdio(ctx)
		} else {
			errCh <- server.ServeHTTP(ctx, opts.httpAddr)
		}
	}()

	select {
	case <-ctx.Done():
		log.Infow("shutdown signal received")
		// PROMPT: Gracefully stop MCP server and app process if policy dictates.
		return 0
	case err := <-errCh:
		if err != nil {
			log.Errorw("server exited with error", "error", err)
			return 1
		}
		return 0
	}
}

func cmdStart(ctx context.Context, log util.Logger, opts options) int {
	// PROMPT: Write bridge.json and launch the app. Print PID on stdout for scripting.
	_, _, _, _ = config.WriteBridgeJSON, process.Controller{}, ctx, log
	return 0
}

func cmdStop(ctx context.Context, log util.Logger, opts options) int {
	// PROMPT: Locate running app (PID file or process lookup) and attempt graceful stop with opts.graceStop.
	_ = ctx
	return 0
}

func cmdKill(ctx context.Context, log util.Logger, opts options) int {
	// PROMPT: Locate running app and force terminate immediately.
	_ = log
	return 0
}

func cmdRestart(ctx context.Context, log util.Logger, opts options) int {
	// PROMPT: Stop then Start preserving launchId and bridge.json.
	_ = opts
	return 0
}

func cmdAttach(ctx context.Context, log util.Logger, opts options) int {
	// PROMPT: Skip launching. Assume mod started GABP and wrote bridge.json. Read it, connect, then run MCP like in run().
	_ = log
	return 0
}

func cmdStatus(ctx context.Context, log util.Logger, opts options) int {
	// PROMPT: Print JSON status: {running:bool, pid:int, gameId, since, mcp:{transport,addr}}
	_ = ctx
	return 0
}

// === Helpers ===

func parseBackoff(s string) (time.Duration, time.Duration, error) {
	// PROMPT: Parse "<min>..( <max> | inf )". Accept units per time.ParseDuration.
	// Examples: "100ms..5s", "1s..30s", "250ms..inf".
	// For now, return defaults; codegen should implement full parser.
	switch {
	case s == "", s == defaultBackoff:
		return 100 * time.Millisecond, 5 * time.Second, nil
	default:
		// PROMPT: Implement real parsing. Fallback to defaults on error.
		return 100 * time.Millisecond, 5 * time.Second, nil
	}
}
