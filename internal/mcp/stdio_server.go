package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"runtime"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/launch"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
	"github.com/pardeike/gabs/internal/version"
)

// Server runs MCP over stdio.
type Server struct {
	log               util.Logger
	tools             map[string]*ToolHandler
	resources         map[string]*ResourceHandler
	games             map[string]process.ControllerInterface // Track running games
	configDir         string                                 // Config directory for bridge files
	configStore       *config.Store                          // Hot-reload snapshot source (nil in tests)
	apiKey            string                                 // API key for HTTP authentication
	mu                sync.RWMutex
	writers           []util.FrameWriter       // Track client connections for notifications
	writersMu         sync.RWMutex             // Protect writers slice
	gameTools         map[string][]string      // Track which tools belong to which games
	gameToolAliases   map[string]gameToolAlias // Resolve strict-safe and legacy names back to GABP names
	gameResources     map[string][]string      // Track which resources belong to which games
	gabpClients       map[string]*gabp.Client  // Track GABP connections per game
	gabpAttention     map[string]*gameAttentionState
	gabpDisconnects   map[string]gabpDisconnectRecord
	bridgeAttachments map[string]bridgeAttachmentRef // Current persisted attachment identity per game (lazy-init)
	starter           *process.SerializedStarter     // Serialized process starter
	gamesConfig       *config.GamesConfig
	instanceID        string
	ownerLease        time.Duration
	stripOutputSchema bool // Strip outputSchema from tools/list responses

	// Background-task lifecycle (round 12 F4): every detached task — async
	// tool mirroring, attention setup, and the attachment lease refresher —
	// registers with bgWG and honors shutdownCh, so Shutdown() can cancel and
	// JOIN them before a test's TempDir teardown (or a real server exit). A
	// lease/mirroring goroutine writing runtime.json during RemoveAll is the
	// TempDir-cleanup race this closes.
	bgWG         sync.WaitGroup
	disconnectWG sync.WaitGroup // in-flight peer-close handlers (joined by Shutdown)
	shutdownCh   chan struct{}
	shutdownOnce sync.Once
	// ownedTempDir is a config directory this constructor created (test-only);
	// Shutdown removes it after the joins so it is not leaked (round 13 F6).
	ownedTempDir string

	// newController builds the process controller for a start. Injectable so a
	// test can supply a controller with DETERMINISTIC liveness (exit before
	// Stage 4, or verified-then-Stage-5-death) — a real subprocess's timing is
	// non-deterministic under -race and would credit or skip the Stage-4 start
	// by luck (round 12 F5). Defaults to process.NewController.
	newController func() process.ControllerInterface
}

type gabpDisconnectRecord struct {
	At      time.Time
	Message string
}

var serverInstanceCounter uint64

const ServerInstructions = `GABS controls configured local games and mirrors connected GABP bridge tools into MCP. Start with games_list or games_status, then use games_start or games_connect with gameId.
For game-specific actions, call games_tool_names with brief=true, inspect one tool with games_tool_detail, then invoke it through games_call_tool.
Prefer strict-safe tool names such as games_start; dotted aliases remain accepted. Public tools/list is kept stable and core-only, so retry games_tool_names or connect before assuming a bridge tool is missing.`

type gameAlreadyActiveError struct {
	status string
}

func (e *gameAlreadyActiveError) Error() string {
	switch e.status {
	case process.RuntimeStateStatusStarting:
		return "game launch is already in progress"
	default:
		return "game is already running"
	}
}

func (e *gameAlreadyActiveError) ToolMessage(game config.GameConfig) string {
	switch e.status {
	case process.RuntimeStateStatusStarting:
		return fmt.Sprintf("Game '%s' (%s) is already starting. Wait for launch to finish, then use games_connect if you need to attach to the existing instance.", game.ID, game.Name)
	default:
		return fmt.Sprintf("Game '%s' (%s) is already running. Use games_status or games_connect instead of starting it again.", game.ID, game.Name)
	}
}

// ToolHandler represents a tool handler function
type ToolHandler struct {
	Tool    Tool
	Handler func(args map[string]interface{}) (*ToolResult, error)
}

// ResourceHandler represents a resource handler function
type ResourceHandler struct {
	Resource Resource
	Handler  func() ([]Content, error)
}

// SetControllerFactoryForTesting injects a deterministic controller builder
// (round 12 F5). Tests use it to prove exit before Stage 4 (no workloadStart)
// or a Stage-4-verified-then-Stage-5-death without depending on subprocess
// timing under -race.
func (s *Server) SetControllerFactoryForTesting(f func() process.ControllerInterface) {
	s.newController = f
}

// Shutdown cancels and JOINS every background task the server started
// (async mirroring, attention setup, attachment lease refresh) — round 12 F4.
// It signals shutdownCh, disconnects live GABP clients to unblock any task
// parked in a blocking read/RPC, then waits for all registered goroutines to
// return. Idempotent. Tests register this via t.Cleanup so no background write
// can race TempDir teardown; a real server calls it on exit.
func (s *Server) Shutdown() {
	// Signal shutdown AND snapshot/detach clients under one lock so it is
	// atomic with dispatchGABPDisconnect's guarded Add: a peer-close handler
	// either registered with disconnectWG before this point (and is joined
	// below) or sees shutdownCh closed and no-ops. No clearBridgeAttachment
	// write can escape the join (round 12 F4).
	s.mu.Lock()
	s.shutdownOnce.Do(func() { close(s.shutdownCh) })
	clients := make([]*gabp.Client, 0, len(s.gabpClients))
	for id, c := range s.gabpClients {
		if c != nil {
			clients = append(clients, c)
		}
		delete(s.gabpClients, id)
	}
	s.bridgeAttachments = nil
	s.mu.Unlock()

	// Close clients to unblock any task parked in a blocking read/RPC, then
	// JOIN both the background tasks and any in-flight disconnect handler.
	for _, c := range clients {
		_ = c.Close()
	}
	s.disconnectWG.Wait()
	s.bgWG.Wait()

	// Only after every background task has joined is the constructor-owned temp
	// directory safe to remove — a lingering write would otherwise recreate it
	// (round 13 F6). A caller-provided SetConfigDir directory is never touched.
	if s.ownedTempDir != "" {
		_ = os.RemoveAll(s.ownedTempDir)
	}
}

// admitBackgroundTask registers a background task with the shutdown join,
// atomic with Shutdown closing admission (round 13 F3). The shutdownCh check
// and bgWG.Add happen together under s.mu — the SAME lock Shutdown holds when
// it closes shutdownCh — so no positive Add can race bgWG.Wait(). Returns
// false (registering nothing) once shutdown has begun; the caller must then
// NOT start the goroutine. On true, the caller owns exactly one bgWG.Done().
func (s *Server) admitBackgroundTask() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.shutdownCh:
		return false
	default:
	}
	s.bgWG.Add(1)
	return true
}

func NewServer(log util.Logger) *Server {
	return &Server{
		log:             log,
		tools:           make(map[string]*ToolHandler),
		resources:       make(map[string]*ResourceHandler),
		games:           make(map[string]process.ControllerInterface),
		configDir:       "", // Will be set by SetConfigDir
		writers:         make([]util.FrameWriter, 0),
		gameTools:       make(map[string][]string),
		gameToolAliases: make(map[string]gameToolAlias),
		gameResources:   make(map[string][]string),
		gabpClients:     make(map[string]*gabp.Client),
		gabpAttention:   make(map[string]*gameAttentionState),
		gabpDisconnects: make(map[string]gabpDisconnectRecord),
		starter:         process.NewSerializedStarter(), // Initialize serialized starter
		instanceID:      newServerInstanceID(),
		ownerLease:      (&config.GamesConfig{}).GetSessionOwnerLease(),
		shutdownCh:      make(chan struct{}),
		newController:   func() process.ControllerInterface { return process.NewController() },
	}
}

// NewServerForTesting creates a server with shorter timeouts for testing.
// configDir defaults to a FRESH isolated temp directory, never the empty
// string — an empty configDir resolves to the real ~/.gabs (config.NewConfigPaths),
// so a test that forgot SetConfigDir, or any code path that runs before it,
// would read and write the user's actual GABS state (round 12 F4). Tests that
// call SetConfigDir override this harmless default.
func NewServerForTesting(log util.Logger) *Server {
	isolated, err := os.MkdirTemp("", "gabs-test-isolated-")
	if err != nil {
		// A test host without a writable temp dir cannot run these tests
		// safely; failing loud beats silently falling back to ~/.gabs.
		panic(fmt.Sprintf("NewServerForTesting: cannot create isolated config dir: %v", err))
	}
	return &Server{
		log:             log,
		tools:           make(map[string]*ToolHandler),
		resources:       make(map[string]*ResourceHandler),
		games:           make(map[string]process.ControllerInterface),
		configDir:       isolated,
		ownedTempDir:    isolated,
		writers:         make([]util.FrameWriter, 0),
		gameTools:       make(map[string][]string),
		gameToolAliases: make(map[string]gameToolAlias),
		gameResources:   make(map[string][]string),
		gabpClients:     make(map[string]*gabp.Client),
		gabpAttention:   make(map[string]*gameAttentionState),
		gabpDisconnects: make(map[string]gabpDisconnectRecord),
		starter:         process.NewSerializedStarterForTesting(), // Use testing timeouts
		instanceID:      newServerInstanceID(),
		ownerLease:      (&config.GamesConfig{}).GetSessionOwnerLease(),
		shutdownCh:      make(chan struct{}),
		newController:   func() process.ControllerInterface { return process.NewController() },
	}
}

func newServerInstanceID() string {
	seq := atomic.AddUint64(&serverInstanceCounter, 1)
	return fmt.Sprintf("%d-%d-%d", os.Getpid(), time.Now().UnixNano(), seq)
}

func (s *Server) runtimeOwnerLeaseDuration() time.Duration {
	if s.ownerLease > 0 {
		return s.ownerLease
	}
	return (&config.GamesConfig{}).GetSessionOwnerLease()
}

func (s *Server) runtimeOwnerLeaseForOperation(operationTimeout time.Duration) time.Duration {
	lease := s.runtimeOwnerLeaseDuration()
	if operationTimeout <= 0 {
		return lease
	}
	operationLease := operationTimeout + 5*time.Second
	if operationLease > lease {
		return operationLease
	}
	return lease
}

func (s *Server) gameConfigForRuntimeOwnership(gameID string) config.GameConfig {
	if s.gamesConfig != nil {
		if game, exists := s.gamesConfig.GetGame(gameID); exists {
			return *game
		}
	}
	return config.GameConfig{ID: gameID}
}

func (s *Server) saveRuntimeOwnerLease(game config.GameConfig, state *process.RuntimeState, operationTimeout time.Duration) (*process.RuntimeState, error) {
	now := time.Now().UTC()
	lease := s.runtimeOwnerLeaseForOperation(operationTimeout)

	if state != nil && state.SchemaVersion >= process.RuntimeSchemaVersion && state.LaunchID != "" {
		// Current-schema claims: the refresh is fenced to the launch the
		// caller loaded — ownership on a successor claim is never touched —
		// and mutates only its own fields under the transition lock, so
		// concurrent fenced writes (attachment records, phase promotions)
		// survive (design/06). Pinned fields stay pinned: an intentionally
		// empty stopProcessName is part of the launch snapshot and is never
		// refilled from current config (design/07; only M2.8's explicit
		// legacy normalization may consult config).
		expectedLaunchID := state.LaunchID
		updated, err := process.FencedTransition(game.ID, s.configDir, expectedLaunchID, "", func(st *process.RuntimeState) error {
			st.Status = process.RuntimeStateStatusRunning
			*st = process.RefreshRuntimeOwnerLease(*st, os.Getpid(), s.instanceID, lease, now)
			return nil
		})
		if err != nil {
			if errors.Is(err, process.ErrFencingViolation) || errors.Is(err, process.ErrNoRuntimeClaim) {
				return nil, fmt.Errorf("the runtime claim for '%s' was replaced or removed while preparing this operation; re-check games_status and retry", game.ID)
			}
			return nil, err
		}
		return updated, nil
	}

	// Legacy path (no claim, or a pre-profile claim): create or refresh the
	// ownership record under the transition lock so it can never overwrite
	// a current-schema claim published in between.
	lock, err := process.AcquireTransitionLock(game.ID, s.configDir, lifecycleLockTimeout)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	current, err := process.LoadRuntimeState(game.ID, s.configDir)
	if err != nil {
		return nil, err
	}
	if current != nil && current.SchemaVersion >= process.RuntimeSchemaVersion {
		return nil, fmt.Errorf("a launch claim for '%s' was published while preparing this operation; re-check games_status and retry", game.ID)
	}

	updatedState := process.RuntimeState{
		GameID:          game.ID,
		Status:          process.RuntimeStateStatusRunning,
		OwnerPID:        os.Getpid(),
		OwnerInstanceID: s.instanceID,
		StopProcessName: game.StopProcessName,
	}
	if current != nil {
		updatedState = *current
		updatedState.GameID = game.ID
		updatedState.Status = process.RuntimeStateStatusRunning
		if updatedState.StopProcessName == "" {
			updatedState.StopProcessName = game.StopProcessName
		}
	}
	updatedState = process.RefreshRuntimeOwnerLease(updatedState, os.Getpid(), s.instanceID, lease, now)
	if err := process.SaveRuntimeState(game.ID, s.configDir, updatedState); err != nil {
		return nil, err
	}
	return &updatedState, nil
}

func (s *Server) restoreRuntimeOwnerAfterFailedConnect(gameID string, previousState *process.RuntimeState) {
	currentState, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil {
		s.log.Warnw("failed to inspect runtime ownership after connect failure", "gameId", gameID, "error", err)
		return
	}
	if currentState == nil || currentState.OwnerPID != os.Getpid() || currentState.OwnerInstanceID != s.instanceID {
		return
	}

	if currentState.SchemaVersion >= process.RuntimeSchemaVersion {
		// Current-schema claims: the rollback is fenced to the exact launch
		// whose ownership this connect refreshed — owner PID/instance alone
		// cannot distinguish a successor launch from the same server — and
		// it restores ownership fields only, never the whole stale snapshot,
		// and never deletes a launch claim.
		if previousState == nil || previousState.LaunchID != currentState.LaunchID {
			return
		}
		if _, err := process.FencedTransition(gameID, s.configDir, previousState.LaunchID, "", func(st *process.RuntimeState) error {
			st.Status = previousState.Status
			st.OwnerPID = previousState.OwnerPID
			st.OwnerInstanceID = previousState.OwnerInstanceID
			st.OwnerLeaseUntil = previousState.OwnerLeaseUntil
			st.OwnerLastActive = previousState.OwnerLastActive
			return nil
		}); err != nil && !errors.Is(err, process.ErrFencingViolation) && !errors.Is(err, process.ErrNoRuntimeClaim) {
			s.log.Warnw("failed to restore runtime ownership after connect failure", "gameId", gameID, "error", err)
		}
		return
	}

	if previousState == nil {
		if err := process.RemoveRuntimeState(gameID, s.configDir); err != nil {
			s.log.Warnw("failed to clear runtime ownership after connect failure", "gameId", gameID, "error", err)
		}
		return
	}
	if previousState.SchemaVersion >= process.RuntimeSchemaVersion {
		// A schema-0 ownership record replaced a launch claim? Impossible
		// via the fenced refresh; do not guess.
		return
	}

	if err := process.SaveRuntimeState(gameID, s.configDir, *previousState); err != nil {
		s.log.Warnw("failed to restore runtime ownership after connect failure", "gameId", gameID, "error", err)
	}
}

func (s *Server) runtimeOwnershipBlockedResult(gameID, action string, state *process.RuntimeState) *ToolResult {
	leaseUntil := process.RuntimeOwnerLeaseExpiresAt(state, s.runtimeOwnerLeaseDuration())
	leaseText := ""
	if !leaseUntil.IsZero() {
		leaseText = fmt.Sprintf(" until %s", leaseUntil.Format(time.RFC3339))
	}

	structured := map[string]interface{}{
		"executed":      false,
		"status":        "blocked_by_active_runtime_owner",
		"gameId":        gameID,
		"ownerPID":      state.OwnerPID,
		"ownerInstance": state.OwnerInstanceID,
		"nextActions": []map[string]interface{}{
			mcpNextAction("games_status", map[string]interface{}{"gameId": gameID}, "Inspect the current runtime owner and bridge status."),
			mcpNextAction("games_connect", map[string]interface{}{"gameId": gameID}, "Retry after the active owner lease expires, or use forceTakeover only when intentionally moving control now."),
		},
	}
	if !leaseUntil.IsZero() {
		structured["ownerLeaseUntil"] = leaseUntil.Format(time.RFC3339)
		structured["ownerLeaseRemainingMs"] = time.Until(leaseUntil).Milliseconds()
	}

	return &ToolResult{
		Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("Game '%s' is currently owned by another active GABS session (pid %d%s). This session did not execute %s to avoid competing bridge clients.", gameID, state.OwnerPID, leaseText, action),
		}},
		StructuredContent: structured,
		IsError:           true,
	}
}

func (s *Server) ensureRuntimeOwnershipForGameCall(gameID, action string, operationTimeout time.Duration) *ToolResult {
	runtimeState, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil {
		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to inspect runtime ownership for '%s': %v", gameID, err)}},
			IsError: true,
		}
	}

	now := time.Now().UTC()
	if process.RuntimeStateOwnedByAnotherActiveOwner(runtimeState, os.Getpid(), s.instanceID, s.runtimeOwnerLeaseDuration(), now) {
		return s.runtimeOwnershipBlockedResult(gameID, action, runtimeState)
	}

	game := s.gameConfigForRuntimeOwnership(gameID)
	if _, err := s.saveRuntimeOwnerLease(game, runtimeState, operationTimeout); err != nil {
		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to claim runtime ownership for '%s': %v", gameID, err)}},
			IsError: true,
		}
	}

	return nil
}

// strictArgs rejects unknown tool arguments with the stable unknown_argument
// code, the offending path, and the sorted allowed names. Every core handler
// enforces this independently of client-side schema validation
// (design/10-mcp-surface.md).
func strictArgs(args map[string]interface{}, allowed ...string) *ToolResult {
	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = true
	}
	var unknown []string
	for k := range args {
		if !allowedSet[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	sortedAllowed := append([]string(nil), allowed...)
	sort.Strings(sortedAllowed)
	return &ToolResult{
		Content: []Content{{Type: "text", Text: fmt.Sprintf(
			"Unknown argument %q. Allowed arguments: %s", unknown[0], strings.Join(sortedAllowed, ", "))}},
		IsError: true,
		StructuredContent: map[string]interface{}{
			"code":    "unknown_argument",
			"path":    "/" + unknown[0],
			"unknown": unknown,
			"allowed": sortedAllowed,
			// A wrong request is call-class — fix the call, not the config
			// (design/08; round 11 P1-1). It still carries the neutral track-
			// record line: no context exists to hash (round 12 F2).
			"causeClass":  process.CauseCall,
			"trackRecord": process.TrackRecordLine(nil),
		},
	}
}

func parseOptionalPositiveIntValue(raw interface{}, key string) (int, bool, *ToolResult) {
	if raw == nil {
		return 0, false, nil
	}

	var value int
	switch typed := raw.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil {
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
				IsError: true,
			}
		}
		value = int(parsed)
	case float64:
		if typed != float64(int(typed)) {
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
				IsError: true,
			}
		}
		value = int(typed)
	case int:
		value = typed
	case int32:
		value = int(typed)
	case int64:
		value = int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
				IsError: true,
			}
		}
		value = parsed
	default:
		return 0, false, &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
			IsError: true,
		}
	}

	if value <= 0 {
		return 0, false, nil
	}

	return value, true, nil
}

func parseOptionalBoolArg(args map[string]interface{}, key string) (bool, bool, *ToolResult) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return false, false, nil
	}

	switch typed := raw.(type) {
	case bool:
		return typed, true, nil
	case string:
		value := strings.TrimSpace(strings.ToLower(typed))
		switch value {
		case "true":
			return true, true, nil
		case "false":
			return false, true, nil
		}
	}

	return false, false, &ToolResult{
		Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be a boolean", key)}},
		IsError: true,
	}
}

func parseOptionalTimeoutSecondsArg(args map[string]interface{}, key string, defaultValue time.Duration) (time.Duration, *ToolResult) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return defaultValue, nil
	}

	seconds, hasValue, invalidArg := parseOptionalPositiveIntValue(raw, key)
	if invalidArg != nil {
		return defaultValue, invalidArg
	}
	if !hasValue {
		return defaultValue, nil
	}

	return time.Duration(seconds) * time.Second, nil
}

func deriveMirroredToolCallTimeout(args map[string]interface{}, defaultValue time.Duration) (time.Duration, *ToolResult) {
	timeout := defaultValue

	if timeoutMs, hasValue, invalidArg := parseOptionalPositiveIntValue(args["timeoutMs"], "timeoutMs"); invalidArg != nil {
		return defaultValue, invalidArg
	} else if hasValue {
		candidate := time.Duration(timeoutMs)*time.Millisecond + (5 * time.Second)
		if candidate > timeout {
			timeout = candidate
		}
	}

	if timeoutSeconds, hasValue, invalidArg := parseOptionalPositiveIntValue(args["timeout"], "timeout"); invalidArg != nil {
		return defaultValue, invalidArg
	} else if hasValue {
		candidate := time.Duration(timeoutSeconds) * time.Second
		if candidate > timeout {
			timeout = candidate
		}
	}

	return timeout, nil
}

var maxSynchronousStartupGABPWait = 20 * time.Second

type bridgeEndpoint struct {
	Port   int
	Token  string
	Source string
	PID    int
}

type startupConnectResult struct {
	Connected               bool
	Error                   error
	Wait                    time.Duration
	GameStillRunning        bool
	ProcessExitedDuringGABP bool
}

func bridgeEndpointInUseResult(game config.GameConfig, endpointErr *config.BridgeEndpointInUseError) *ToolResult {
	gameArg := map[string]interface{}{"gameId": game.ID}
	resetArg := map[string]interface{}{"gameId": game.ID, "resetEndpoint": true}
	return &ToolResult{
		Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("GABS endpoint cache for game '%s' uses port %d, but that port is already listening. This session did not start another process because the cached endpoint may belong to an already-running game-side bridge. Use games_connect to attach, or start again with resetEndpoint only after confirming that the cached endpoint should be rotated.", game.ID, endpointErr.Port),
		}},
		StructuredContent: map[string]interface{}{
			"code":   "endpoint_unavailable",
			"gameId": game.ID,
			"status": "endpoint_cache_in_use",
			"port":   endpointErr.Port,
			"nextActions": []map[string]interface{}{
				mcpNextAction("games_status", gameArg, "Inspect runtime ownership and process status."),
				mcpNextAction("games_connect", gameArg, "Attach if an already-running game-side bridge owns the cached endpoint."),
				mcpNextAction("games_start", resetArg, "Rotate the endpoint cache and start a new process only after confirming the cached endpoint is not an existing game."),
			},
		},
		IsError: true,
	}
}

func boundedStartupGABPWait(total time.Duration) time.Duration {
	if total <= 0 {
		return 0
	}
	if total < maxSynchronousStartupGABPWait {
		return total
	}
	return maxSynchronousStartupGABPWait
}

func remainingStartupGABPWait(total, alreadyWaited time.Duration) time.Duration {
	if total <= 0 || alreadyWaited >= total {
		return 0
	}
	return total - alreadyWaited
}

// RegisterTool registers a tool with its handler, applying normalization if configured
func (s *Server) RegisterTool(tool Tool, handler func(args map[string]interface{}) (*ToolResult, error)) {
	s.RegisterToolWithConfig(tool, handler, nil)
}

// RegisterToolWithConfig registers a tool with its handler, applying normalization based on config
func (s *Server) RegisterToolWithConfig(tool Tool, handler func(args map[string]interface{}) (*ToolResult, error), normalizationConfig *config.ToolNormalizationConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Apply normalization if configured
	registeredTool := tool
	if normalizationConfig != nil && normalizationConfig.EnableOpenAINormalization {
		normalizedResult := util.NormalizeToolNameForOpenAI(tool.Name, normalizationConfig.MaxToolNameLength)

		if normalizedResult.WasNormalized {
			// Store original name in metadata
			if registeredTool.Meta == nil {
				registeredTool.Meta = make(map[string]interface{})
			}
			registeredTool.Meta["originalName"] = normalizedResult.OriginalName

			// Update the tool name to the normalized version
			registeredTool.Name = normalizedResult.NormalizedName

			// Optionally preserve original name in description
			if normalizationConfig.PreserveOriginalName && registeredTool.Description != "" {
				registeredTool.Description = fmt.Sprintf("%s (Original: %s)", registeredTool.Description, normalizedResult.OriginalName)
			}

			s.log.Debugw("normalized tool name for OpenAI compatibility",
				"original", normalizedResult.OriginalName,
				"normalized", normalizedResult.NormalizedName)
		}
	}

	s.tools[registeredTool.Name] = &ToolHandler{
		Tool:    registeredTool,
		Handler: handler,
	}
}

// RegisterResource registers a resource with its handler
func (s *Server) RegisterResource(resource Resource, handler func() ([]Content, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[resource.URI] = &ResourceHandler{
		Resource: resource,
		Handler:  handler,
	}
}

// SetConfigDir sets the configuration directory for bridge files
func (s *Server) SetConfigDir(configDir string) {
	s.configDir = configDir
}

// configUnavailableResult reports a startup-invalid configuration with no
// last-known-good snapshot.
func configUnavailableResult(cerr *config.ConfigError) *ToolResult {
	msg := "Configuration is invalid and no last-known-good snapshot exists."
	if cerr != nil {
		msg = fmt.Sprintf("%s %v", msg, cerr.Err)
	}
	return &ToolResult{
		Content: []Content{{Type: "text", Text: msg}},
		IsError: true,
		StructuredContent: map[string]interface{}{
			"code": "config_invalid",
		},
	}
}

// SetConfigStore enables hot config reload: handlers fetch a fresh snapshot
// per call instead of using the pointer captured at registration. Without a
// store (tests), the startup config doubles as an immutable snapshot.
func (s *Server) SetConfigStore(store *config.Store) {
	s.configStore = store
	// Prime the last-known-good snapshot immediately: startup validated the
	// config already, and without priming, a config that turns invalid
	// before the first tool call would leave the store with no
	// last-known-good to serve read-only callers.
	if store != nil {
		_, _ = store.Snapshot()
	}
}

// currentGamesConfig returns the per-call configuration plus any config
// error. Semantics per design/09: (cfg, nil) valid; (cfg, err) disk invalid,
// last-known-good served — reads proceed, starts must refuse; (nil, err)
// startup-invalid with no last-known-good.
func (s *Server) currentGamesConfig() (*config.GamesConfig, string, *config.ConfigError) {
	if s.configStore == nil {
		return s.gamesConfig, "startup", nil
	}
	snap, cerr := s.configStore.Snapshot()
	if snap == nil {
		return nil, "", cerr
	}
	return snap.Config, snap.Revision, cerr
}

// currentSnapshot returns the launch-resolution snapshot for games_start.
func (s *Server) currentSnapshot() (*config.Snapshot, *config.ConfigError) {
	if s.configStore == nil {
		return &config.Snapshot{Config: s.gamesConfig, Revision: "startup", ConfigDir: s.configDir}, nil
	}
	return s.configStore.Snapshot()
}

// SetAPIKey sets the API key for HTTP authentication
func (s *Server) SetAPIKey(apiKey string) {
	s.apiKey = apiKey
}

// RegisterGameManagementTools registers the game management tools for the new architecture
func (s *Server) RegisterGameManagementTools(gamesConfig *config.GamesConfig, backoffMin, backoffMax time.Duration) {
	s.stripOutputSchema = gamesConfig.StripOutputSchema
	s.gamesConfig = gamesConfig
	s.ownerLease = gamesConfig.GetSessionOwnerLease()
	normalizationConfig := gamesConfig.GetToolNormalization()
	if gamesConfig.Timeouts != nil && gamesConfig.Timeouts.Startup != nil {
		processStartTimeout, gabpConnectTimeout := gamesConfig.GetStartupTimeouts()
		s.starter.SetTimeouts(processStartTimeout, gabpConnectTimeout)
	}

	// games_list tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.list",
		Description: "List all configured game IDs",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties":           map[string]interface{}{},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args); res != nil {
			return res, nil
		}
		gamesConfig, configRevision, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		games := gamesConfig.ListGames()

		var content strings.Builder
		if len(games) == 0 {
			content.WriteString("No games configured. Use the CLI to add games: gabs games add <id>")
		} else {
			for i, game := range games {
				if i > 0 {
					content.WriteString("\n")
				}
				content.WriteString(game.ID)
			}
		}

		gameItems := make([]map[string]interface{}, 0, len(games))
		for _, game := range games {
			item := map[string]interface{}{
				"gameId": game.ID,
				"name":   game.Name,
			}
			if game.Description != "" {
				item["description"] = game.Description
			}
			if len(game.Profiles) > 0 {
				item["profiles"] = sortedProfileNames(game.Profiles)
				item["defaultProfile"] = game.DefaultProfile
			}
			if n := len(gameConfigWarnings(gamesConfig, game.ID)); n > 0 {
				item["warningCount"] = n
			}
			gameItems = append(gameItems, item)
		}

		structured := map[string]interface{}{
			"count":                 len(games),
			"games":                 gameItems,
			"currentConfigRevision": configRevision,
		}
		attachConfigHealth(structured, gamesConfig, cfgErr)
		if len(games) == 0 {
			structured["nextActions"] = []map[string]interface{}{
				{
					"command": "gabs games add <id>",
					"reason":  "Configure a game before using MCP game-management tools.",
				},
			}
		} else {
			structured["nextActions"] = []map[string]interface{}{
				mcpNextAction("games_status", map[string]interface{}{}, "Check which configured games are running or connected."),
			}
		}

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: content.String()}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games.show tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.show",
		Description: "Show detailed configuration and validation status for a specific game",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target to show details for",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "gameId"); res != nil {
			return res, nil
		}
		gamesConfig, configRevision, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameIdOrTarget, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, resolveFail := resolveGameResult(gamesConfig, gameIdOrTarget)
		if resolveFail != nil {
			return resolveFail, nil
		}

		var content strings.Builder
		content.WriteString(fmt.Sprintf("Game Configuration: %s\n\n", game.ID))
		content.WriteString(fmt.Sprintf("  ID: %s (%s)\n", game.ID, game.Name))
		content.WriteString(fmt.Sprintf("  Use gameId: '%s' (or target: '%s')\n", game.ID, game.Target))
		content.WriteString(fmt.Sprintf("  Launch: %s\n", game.LaunchMode))

		if game.WorkingDir != "" {
			content.WriteString(fmt.Sprintf("  Working Directory: %s\n", game.WorkingDir))
		}
		if len(game.Args) > 0 {
			content.WriteString(fmt.Sprintf("  Arguments: %s\n", strings.Join(game.Args, " ")))
		}

		// Validation status for launcher-based games
		if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
			content.WriteString("\nGame Termination Configuration:\n")
			if game.StopProcessName != "" {
				content.WriteString(fmt.Sprintf("  ✓ Configured for proper game termination (process: %s)\n", game.StopProcessName))
			} else {
				content.WriteString(fmt.Sprintf("  ⚠️  Missing stopProcessName - GABS can start but cannot properly stop %s games\n", game.LaunchMode))
				content.WriteString(fmt.Sprintf("     Add stopProcessName to your game configuration for proper termination.\n"))
			}
		} else if game.StopProcessName != "" {
			content.WriteString(fmt.Sprintf("  Stop Process Name: %s\n", game.StopProcessName))
		}

		if game.Description != "" {
			content.WriteString(fmt.Sprintf("\nDescription: %s\n", game.Description))
		}

		status := s.checkGameStatus(game.ID)
		validationWarnings := gameValidationWarnings(*game)
		if len(validationWarnings) > 0 {
			content.WriteString("\nConfiguration Warnings:\n")
			for _, warning := range validationWarnings {
				content.WriteString(fmt.Sprintf("  - %s\n", warning))
			}
		}
		structured := map[string]interface{}{
			"game":               gameConfigStructured(*game),
			"status":             status,
			"statusDescription":  s.getStatusDescriptionFromStatus(status, game),
			"validationWarnings": validationWarnings,
			"nextActions":        s.nextActionsForGameStatus(*game, status, len(s.getGameSpecificTools(game.ID))),
		}
		structured["currentConfigRevision"] = configRevision
		// activeConfigRevision: the revision the RUNNING launch was resolved
		// from (persisted in the claim), distinct from what the next start
		// would use (design/09, M1.11).
		if rs, rsErr := process.LoadRuntimeState(game.ID, s.configDir); rsErr == nil && rs != nil && rs.ConfigRevision != "" {
			structured["activeConfigRevision"] = rs.ConfigRevision
		}
		if len(game.Profiles) > 0 {
			structured["profiles"] = profilesStructured(game.Profiles)
			structured["defaultProfile"] = game.DefaultProfile
		}
		if len(game.LaunchInputs) > 0 {
			structured["launchInputs"] = launchInputsStructured(game.LaunchInputs)
		}
		// Per-profile track record (design/08; round 10 P2-12): proof and
		// counters for each launchable context, with an edited context read
		// as never-proven for the settings the next start would use.
		if snap, snapErr := s.currentSnapshot(); snapErr == nil {
			if tr := s.buildTrackRecordSummary(snap, *game); len(tr) > 0 {
				structured["trackRecord"] = tr
			}
		}
		if warns := gameConfigWarnings(gamesConfig, game.ID); len(warns) > 0 {
			structured["configWarnings"] = warns
		}
		attachConfigHealth(structured, gamesConfig, cfgErr)

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: content.String()}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games_status tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.status",
		Description: "Check the status of one or more games using game ID or launch target",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target to check (optional, checks all if not provided)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "gameId"); res != nil {
			return res, nil
		}
		gamesConfig, configRevision, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameIdOrTarget, hasGameID := args["gameId"].(string)

		var content strings.Builder
		if hasGameID {
			// Check specific game
			game, resolveFail := resolveGameResult(gamesConfig, gameIdOrTarget)
			if resolveFail != nil {
				return resolveFail, nil
			}

			// Get status once to avoid double mutex lock
			status, statusEv := s.checkGameStatusObserved(game.ID)
			statusDesc := s.getStatusDescriptionFromStatus(status, game)
			statusItem := s.gameStatusStructured(*game, status)
			statusItem["currentConfigRevision"] = configRevision
			attachStatusEvidence(statusItem, statusEv)
			if rs, rsErr := process.LoadRuntimeState(game.ID, s.configDir); rsErr == nil && rs != nil {
				if rs.ConfigRevision != "" {
					statusItem["activeConfigRevision"] = rs.ConfigRevision
				}
				attachRuntimeLifecycle(statusItem, rs)
			}
			attachConfigHealth(statusItem, gamesConfig, cfgErr)
			content.WriteString(fmt.Sprintf("**%s** (%s): %s\n", game.ID, game.Name, statusDesc))
			if diagnosticMessage := gameStateDiagnosticMessage(statusItem); diagnosticMessage != "" {
				content.WriteString(fmt.Sprintf("\nDiagnosis: %s\n", diagnosticMessage))
			}
			if disconnectNote := s.describeLastGABPDisconnect(game.ID); disconnectNote != "" {
				content.WriteString(fmt.Sprintf("\n%s\n", disconnectNote))
			}

			// Add helpful info for launcher games ONLY when we cannot track them
			if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
				if status == "launcher-triggered" {
					// Only show the warning if we don't have stopProcessName configured
					if game.StopProcessName == "" {
						content.WriteString(fmt.Sprintf("\nNote: %s game was launched, but GABS cannot track whether it's still running because no 'stopProcessName' is configured.\nCheck Steam/Epic or your system processes to verify the actual game status.\n", game.LaunchMode))
					}
				}
			}
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: content.String()}},
				StructuredContent: statusItem,
			}, nil
		} else {
			// Check all games. Probes run concurrently, each bounded by its
			// own hook timeout (design/10): one game's slow pinned status
			// hook must not serialize the whole summary. checkGameStatus
			// holds no server lock while probing, so workers are safe.
			games := gamesConfig.ListGames()
			content.WriteString("Game Status Summary:\n\n")
			type gameStatusRow struct {
				desc string
				item map[string]interface{}
			}
			rows := make([]gameStatusRow, len(games))
			sem := make(chan struct{}, 8)
			var wg sync.WaitGroup
			for i := range games {
				wg.Add(1)
				go func(i int, game config.GameConfig) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					status, statusEv := s.checkGameStatusObserved(game.ID)
					statusItem := s.gameStatusStructured(game, status)
					attachStatusEvidence(statusItem, statusEv)
					if rs, rsErr := process.LoadRuntimeState(game.ID, s.configDir); rsErr == nil && rs != nil {
						if rs.ConfigRevision != "" {
							statusItem["activeConfigRevision"] = rs.ConfigRevision
						}
						attachRuntimeLifecycle(statusItem, rs)
					}
					rows[i] = gameStatusRow{desc: s.getStatusDescriptionFromStatus(status, &game), item: statusItem}
				}(i, games[i])
			}
			wg.Wait()

			statusItems := make([]map[string]interface{}, 0, len(games))
			for i, game := range games {
				if diagnosticMessage := gameStateDiagnosticMessage(rows[i].item); diagnosticMessage != "" {
					content.WriteString(fmt.Sprintf("• **%s**: %s — %s\n", game.ID, rows[i].desc, diagnosticMessage))
				} else {
					content.WriteString(fmt.Sprintf("• **%s**: %s\n", game.ID, rows[i].desc))
				}
				statusItems = append(statusItems, rows[i].item)
			}

			structuredAll := map[string]interface{}{
				"count":                 len(statusItems),
				"games":                 statusItems,
				"currentConfigRevision": configRevision,
			}
			attachConfigHealth(structuredAll, gamesConfig, cfgErr)
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: content.String()}},
				StructuredContent: structuredAll,
			}, nil
		}
	}, normalizationConfig)

	// games_start tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.start",
		Description: "Start a configured game using game ID or launch target (e.g., Steam App ID)",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target (Steam App ID, path, etc.)",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Optional total GABP startup connection budget in seconds. The MCP call waits only for a bounded initial slice, then GABS continues connecting in the background.",
				},
				"resetEndpoint": map[string]interface{}{
					"type":        "boolean",
					"description": "Rotate the GABS endpoint cache before launch. Use only after confirming the cached endpoint is not an already-running game-side bridge.",
				},
				"profile": map[string]interface{}{
					"type":        "string",
					"description": "Optional named launch profile. Omitted selects the configured defaultProfile. Discover profiles with games_show.",
				},
				"launchInputs": map[string]interface{}{
					"type":        "object",
					"description": "Declared launch inputs (boolean/string/integer values). Only inputs declared in the game's configuration are accepted; discover them with games_show. Not a substitute for GABP tools.",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "gameId", "launchInputs", "profile", "resetEndpoint", "timeout"); res != nil {
			return res, nil
		}
		snap, cfgErr := s.currentSnapshot()
		if snap == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameIdOrTarget, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}
		if cfgErr != nil {
			// A hot edit that gave a URL-mode game context fields is the
			// specific, stable outcome — not a generic stale-config refusal.
			if issues := modeIncompatibleIssues(cfgErr, gameIdOrTarget); len(issues) > 0 {
				structured := map[string]interface{}{"code": "launch_mode_incompatible", "issues": issues, "invalidRevision": cfgErr.Revision}
				s.attachStructuredFailureAttribution(structured, config.GameConfig{ID: gameIdOrTarget}, "launch_mode_incompatible", historyContext{})
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("The launch mode of %q cannot deliver launch context; remove the incompatible fields or switch modes:\n  %s", gameIdOrTarget, strings.Join(issues, "\n  "))}},
					IsError:           true,
					StructuredContent: structured,
				}, nil
			}
			// Starts are refused on stale config: launching from an outdated
			// snapshot is worse than failing with the exact error (design/09).
			structured := map[string]interface{}{"code": "config_invalid", "lastGoodRevision": snap.Revision, "invalidRevision": cfgErr.Revision}
			s.attachStructuredFailureAttribution(structured, config.GameConfig{ID: gameIdOrTarget}, "config_invalid", historyContext{})
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Configuration on disk is invalid; refusing to start from a stale snapshot. Fix the config (it reloads automatically): %v", cfgErr.Err)}},
				IsError:           true,
				StructuredContent: structured,
			}, nil
		}
		gamesConfig := snap.Config

		game, resolveFail := resolveGameResult(gamesConfig, gameIdOrTarget)
		if resolveFail != nil {
			return resolveFail, nil
		}

		// timeout: integral 1..3600 (release-noted validation tightening).
		var startupGABPTimeout time.Duration
		if raw, exists := args["timeout"]; exists && raw != nil {
			seconds, ok, invalidTimeout := parseOptionalPositiveIntValue(raw, "timeout")
			if invalidTimeout != nil {
				return invalidTimeout, nil
			}
			if !ok || seconds < 1 || seconds > 3600 {
				structured := map[string]interface{}{
					"code": "timeout_out_of_range", "minimum": 1, "maximum": 3600,
				}
				s.attachStructuredFailureAttribution(structured, *game, "timeout_out_of_range", historyContext{})
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: "Argument 'timeout' must be an integer between 1 and 3600 seconds"}},
					IsError:           true,
					StructuredContent: structured,
				}, nil
			}
			startupGABPTimeout = time.Duration(seconds) * time.Second
		}
		resetEndpoint, _, resetEndpointErr := parseOptionalBoolArg(args, "resetEndpoint")
		if resetEndpointErr != nil {
			return resetEndpointErr, nil
		}

		profileArg := ""
		if raw, exists := args["profile"]; exists && raw != nil {
			str, ok := raw.(string)
			if !ok {
				// A wrong-typed argument is a protocol-level invalid parameter,
				// not a lifecycle outcome — it carries no stable code (the
				// exhaustive list has none for it; round 12 F3).
				return &ToolResult{
					Content: []Content{{Type: "text", Text: "Argument 'profile' must be a string"}},
					IsError: true,
				}, nil
			}
			profileArg = str
		}
		var inputsArg map[string]interface{}
		if raw, exists := args["launchInputs"]; exists && raw != nil {
			m, ok := raw.(map[string]interface{})
			if !ok {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: "Argument 'launchInputs' must be an object of declared input values"}},
					IsError: true,
				}, nil
			}
			inputsArg = m
		}

		resolved, rerr := launch.Resolve(snap, launch.Request{
			GameID:  game.ID,
			Profile: profileArg,
			Inputs:  inputsArg,
		}, launch.Options{
			InheritedEnv:       os.Environ(),
			CaseInsensitiveEnv: runtime.GOOS == "windows",
		})
		if rerr != nil {
			structured := map[string]interface{}{"code": rerr.Code, "gameId": game.ID}
			if len(rerr.Candidates) > 0 {
				structured["candidates"] = rerr.Candidates
			}
			if rerr.Code == "profiles_not_configured" {
				structured["requestedProfile"] = profileArg
				structured["configPath"] = s.configFilePathHint()
				structured["documentation"] = "docs/CONFIGURATION.md#profiles"
				structured["note"] = "Config edits apply automatically; no GABS or client restart is needed."
			}
			// Mandatory attribution (round 11 P1-1). Resolution failed, so
			// there is no resolvable context: the class comes from the code
			// alone (call/config) with no track-record line, and nothing is
			// written to history.
			s.attachStructuredFailureAttribution(structured, *game, rerr.Code, historyContext{})
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: rerr.Message}},
				IsError:           true,
				StructuredContent: structured,
			}, nil
		}
		if issues := launch.CheckResolvability(game, resolved); len(issues) > 0 {
			lines := make([]string, 0, len(issues))
			structuredIssues := make([]map[string]interface{}, 0, len(issues))
			for _, is := range issues {
				lines = append(lines, is.String())
				structuredIssues = append(structuredIssues, map[string]interface{}{
					"path": is.JSONPath, "fsPath": is.FSPath, "message": is.Message,
				})
			}
			structured := map[string]interface{}{
				"code":   "launch_spec_unresolvable",
				"gameId": game.ID,
				"issues": structuredIssues,
			}
			// Resolution SUCCEEDED (only the resolvability check failed), so
			// the input-free context coordinates exist: classify proof-adjusted
			// (round 11 P1-1) — a target that vanished after proven starts is
			// environment ("it existed before"), a never-proven one is config
			// ("probably a typo"). computeHistoryContext performs NO mutation.
			unresolvedHC := s.computeHistoryContext(snap, *game, resolved, inputsArg)
			s.attachStructuredFailureAttribution(structured, *game, "launch_spec_unresolvable", unresolvedHC)
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: "Launch spec is unresolvable:\n" + strings.Join(lines, "\n")}},
				IsError:           true,
				StructuredContent: structured,
			}, nil
		}

		validationWarnings := gameValidationWarnings(*game)
		hctx := s.buildHistoryContext(snap, *game, resolved, inputsArg)
		startResult, err := s.startGame(*game, gamesConfig, backoffMin, backoffMax, startupGABPTimeout, resetEndpoint, resolved, hctx)
		if err != nil {
			var refusalErr *startRefusalError
			if errors.As(err, &refusalErr) {
				return s.startRefusalResult(*game, refusalErr, hctx, validationWarnings), nil
			}
			var unobsErr *unobservedStartError
			if errors.As(err, &unobsErr) {
				structured := map[string]interface{}{
					"code":   "unobserved",
					"gameId": game.ID,
					"nextActions": []map[string]interface{}{
						mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Re-check after the store launcher settles."),
					},
				}
				if len(unobsErr.warnings) > 0 {
					structured["startWarnings"] = unobsErr.warnings
				}
				s.finalizeStartFailure(structured, *game, hctx, "unobserved")
				addValidationWarnings(structured, validationWarnings)
				addResolvedContext(structured, resolved)
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("Nothing observable for '%s' within the start budget — the store launcher may be updating the game or showing a dialog (login, EULA); check the desktop and re-check games_status. The claim is kept while the launch may still appear.", game.ID)}},
					StructuredContent: structured,
				}, nil
			}
			var exitedErr *exitedDuringStartError
			if errors.As(err, &exitedErr) {
				structured := map[string]interface{}{
					"code":     "exited_during_start",
					"gameId":   game.ID,
					"exitCode": exitedErr.exitCode,
				}
				if exitedErr.tail != "" {
					structured["outputTail"] = exitedErr.tail
				}
				if exitedErr.hookEvidence != "" {
					structured["hookEvidence"] = exitedErr.hookEvidence
				}
				if len(exitedErr.warnings) > 0 {
					structured["startWarnings"] = exitedErr.warnings
				}
				// exited_during_start is game-class by the evidence-based
				// default (design/05 F6): GABS cannot tell a game crash from a
				// wrapper/container exit at the first process it created, so the
				// caller reads the captured output tail for the actual cause.
				s.finalizeStartFailure(structured, *game, hctx, "exited_during_start")
				addValidationWarnings(structured, validationWarnings)
				addResolvedContext(structured, resolved)
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("'%s' exited during start (exit code %d). This is attributed to the workload; read the output tail below for the exact cause — a game crash, missing/corrupt data or save, mod loader failure, DRM refusing to run outside its launcher, anti-cheat rejecting a modified process, a required online login, or (if this launch is a wrapper/container) the wrapper's own error. Output tail:\n%s", game.ID, exitedErr.exitCode, exitedErr.tail)}},
					IsError:           true,
					StructuredContent: structured,
				}, nil
			}
			var activeErr *gameAlreadyActiveError
			if errors.As(err, &activeErr) {
				status := activeErr.status
				if status == "" {
					status = s.checkGameStatus(game.ID)
				}
				toolCount := len(s.getGameSpecificTools(game.ID))
				structured := map[string]interface{}{
					"gameId":      game.ID,
					"status":      status,
					"toolCount":   toolCount,
					"nextActions": s.nextActionsForGameStatus(*game, status, toolCount),
				}
				if profileArg != "" {
					structured["requestedProfile"] = profileArg
				}
				addValidationWarnings(structured, validationWarnings)
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: activeErr.ToolMessage(*game)}},
					StructuredContent: structured,
				}, nil
			}
			var endpointErr *config.BridgeEndpointInUseError
			if errors.As(err, &endpointErr) {
				return bridgeEndpointInUseResult(*game, endpointErr), nil
			}
			var epErr *endpointUnavailableError
			if errors.As(err, &epErr) {
				structured := map[string]interface{}{
					"code": "endpoint_unavailable", "gameId": game.ID, "detail": epErr.err.Error(),
				}
				s.finalizeStartFailure(structured, *game, hctx, "endpoint_unavailable")
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("Cannot start %s: %v", game.ID, epErr)}},
					IsError:           true,
					StructuredContent: structured,
				}, nil
			}
			var sizeIssue *launch.SpecSizeIssue
			if errors.As(err, &sizeIssue) {
				structured := map[string]interface{}{
					"code": "spec_too_large", "part": sizeIssue.Part, "gameId": game.ID,
				}
				s.finalizeStartFailure(structured, *game, hctx, "spec_too_large")
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("Refusing to start %s: %s", game.ID, sizeIssue.Message)}},
					IsError:           true,
					StructuredContent: structured,
				}, nil
			}
			// A pre-spawn fencing loss aborts process creation deliberately;
			// the controller surfaces it through ProcessError, which now
			// Unwraps — map it to the stable supersession outcome, never
			// spawn_failed (round 10).
			if errors.Is(err, process.ErrFencingViolation) || errors.Is(err, process.ErrNoRuntimeClaim) {
				if refErr, ok := s.supersededStartRefusal(game.ID).(*startRefusalError); ok {
					return s.startRefusalResult(*game, refErr, hctx, validationWarnings), nil
				}
			}
			var procErr *process.ProcessError
			if errors.As(err, &procErr) && (procErr.Type == process.ProcessErrorTypeStart || procErr.Type == process.ProcessErrorTypeConfiguration) {
				// OS process creation failed on a valid resolved spec:
				// spawn_failed with the OS evidence (incl. elevation hint).
				structured := map[string]interface{}{
					"code": "spawn_failed", "gameId": game.ID, "osError": procErr.Err.Error(),
				}
				s.finalizeStartFailure(structured, *game, hctx, "spawn_failed")
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("Failed to start %s: %v", game.ID, err)}},
					IsError:           true,
					StructuredContent: structured,
				}, nil
			}

			// An unexpected internal error that matched no classified branch
			// leaves GABS state unresolved — the authorized state code is
			// blocked_unknown_state, and it carries attribution like every
			// other stable start failure (round 12 F1/F3).
			structured := map[string]interface{}{"code": "blocked_unknown_state", "gameId": game.ID}
			s.attachStructuredFailureAttribution(structured, *game, "blocked_unknown_state", hctx)
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Failed to start %s: %v", game.ID, err)}},
				IsError:           true,
				StructuredContent: structured,
			}, nil
		}

		if startResult != nil && !startResult.GABPConnected {
			message := fmt.Sprintf("Game '%s' (%s) started, but GABP was not ready after %s", game.ID, game.Name, startResult.GABPConnectWait.Round(time.Millisecond))
			if startResult.GABPConnectError != nil {
				message = fmt.Sprintf("%s: %v", message, startResult.GABPConnectError)
			}
			if startResult.BackgroundGABPConnect {
				message = fmt.Sprintf("%s. GABS will keep trying in the background for up to %s. The game may still be loading or the GABP bridge may be missing. Use games_status, then games_connect once the bridge is ready.", message, startResult.BackgroundGABPWait.Round(time.Second))
			} else {
				message = fmt.Sprintf("%s. The game may still be loading or the GABP bridge may be missing. Use games_status, then games_connect once the bridge is ready.", message)
			}
			message = appendValidationWarningText(message, validationWarnings)
			structured := map[string]interface{}{
				"code":              "started_bridge_pending",
				"gameId":            game.ID,
				"processStarted":    startResult.ProcessStarted,
				"gabpConnected":     startResult.GABPConnected,
				"gameStillRunning":  startResult.GameStillRunning,
				"gabpWaitMs":        startResult.GABPConnectWait.Milliseconds(),
				"backgroundConnect": startResult.BackgroundGABPConnect,
				"backgroundWaitMs":  startResult.BackgroundGABPWait.Milliseconds(),
				"gabpError": func() interface{} {
					if startResult.GABPConnectError == nil {
						return nil
					}
					return startResult.GABPConnectError.Error()
				}(),
				"nextActions": []map[string]interface{}{
					mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Verify whether the game is still running."),
					mcpNextAction("games_connect", map[string]interface{}{"gameId": game.ID}, "Connect after the GABP bridge finishes loading."),
				},
			}
			addStartAdoption(structured, &message, startResult)
			addValidationWarnings(structured, validationWarnings)
			addResolvedContext(structured, resolved)
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: message}},
				StructuredContent: structured,
			}, nil
		}

		message := fmt.Sprintf("Game '%s' (%s) started successfully and connected via GABP.", game.ID, game.Name)
		message = appendValidationWarningText(message, validationWarnings)
		structured := map[string]interface{}{
			"code":             "started_connected",
			"gameId":           game.ID,
			"processStarted":   true,
			"gabpConnected":    true,
			"gameStillRunning": true,
			"nextActions": []map[string]interface{}{
				mcpNextAction("games_tool_names", map[string]interface{}{"gameId": game.ID, "brief": true}, "Discover connected game-specific tools."),
			},
		}
		addStartAdoption(structured, &message, startResult)
		addValidationWarnings(structured, validationWarnings)
		addResolvedContext(structured, resolved)
		s.attachStartContextDelivery(structured, game.ID)
		return &ToolResult{
			Content:           []Content{{Type: "text", Text: message}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games.stop tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.stop",
		Description: "Gracefully stop a running game using game ID or launch target",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target to stop",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "gameId"); res != nil {
			return res, nil
		}
		gamesConfig, configRevision, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameIdOrTarget, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, resolveFail := resolveGameResult(gamesConfig, gameIdOrTarget)
		if resolveFail != nil {
			return resolveFail, nil
		}

		// Any persisted claim goes through the design/06 pipeline (legacy
		// claims normalize first); the path below remains for
		// in-memory-only tracking with no claim at all.
		if res := s.lifecycleActionResult(*game, process.OperationActionStop, configRevision); res != nil {
			return res, nil
		}

		err := s.stopGame(*game, false)
		if err != nil {
			// Check if this is a launcher-specific configuration issue
			if strings.Contains(err.Error(), "Configure 'stopProcessName'") {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("⚠️ %s\n\nTo fix this, update your game configuration to include a 'stopProcessName'. Use: gabs games show %s", err.Error(), game.ID)}},
					IsError: true,
				}, nil
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to stop %s: %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' (%s) stopped successfully", game.ID, game.Name)}},
		}, nil
	}, normalizationConfig)

	// games.kill tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.kill",
		Description: "Force terminate a running game using game ID or launch target",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID or launch target to force terminate",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "gameId"); res != nil {
			return res, nil
		}
		gamesConfig, configRevision, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameIdOrTarget, ok := args["gameId"].(string)
		if !ok {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "gameId parameter is required"}},
				IsError: true,
			}, nil
		}

		game, resolveFail := resolveGameResult(gamesConfig, gameIdOrTarget)
		if resolveFail != nil {
			return resolveFail, nil
		}

		if res := s.lifecycleActionResult(*game, process.OperationActionKill, configRevision); res != nil {
			return res, nil
		}

		err := s.stopGame(*game, true)
		if err != nil {
			// Check if this is a launcher-specific configuration issue
			if strings.Contains(err.Error(), "Configure 'stopProcessName'") {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("⚠️ %s\n\nTo fix this, update your game configuration to include a 'stopProcessName'. Use: gabs games show %s", err.Error(), game.ID)}},
					IsError: true,
				}, nil
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to kill %s: %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' (%s) terminated successfully", game.ID, game.Name)}},
		}, nil
	}, normalizationConfig)

	type listedGameTool struct {
		GameID        string
		Tool          Tool
		CanonicalName string
		LocalName     string
	}

	getOptionalStringArg := func(args map[string]interface{}, key string) (string, bool, *ToolResult) {
		raw, exists := args[key]
		if !exists || raw == nil {
			return "", false, nil
		}

		value, ok := raw.(string)
		if !ok {
			return "", false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be a string", key)}},
				IsError: true,
			}
		}

		value = strings.TrimSpace(value)
		if value == "" {
			return "", false, nil
		}

		return value, true, nil
	}

	getOptionalBoolArg := parseOptionalBoolArg

	getOptionalPositiveIntArg := func(args map[string]interface{}, key string) (int, bool, *ToolResult) {
		raw, exists := args[key]
		if !exists || raw == nil {
			return 0, false, nil
		}

		var value int
		switch typed := raw.(type) {
		case json.Number:
			parsed, err := strconv.ParseInt(typed.String(), 10, 64)
			if err != nil {
				return 0, false, &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
					IsError: true,
				}
			}
			value = int(parsed)
		case float64:
			if typed != float64(int(typed)) {
				return 0, false, &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
					IsError: true,
				}
			}
			value = int(typed)
		case int:
			value = typed
		case int32:
			value = int(typed)
		case int64:
			value = int(typed)
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err != nil {
				return 0, false, &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
					IsError: true,
				}
			}
			value = parsed
		default:
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be an integer", key)}},
				IsError: true,
			}
		}

		if value <= 0 {
			return 0, false, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Argument '%s' must be greater than zero", key)}},
				IsError: true,
			}
		}

		return value, true, nil
	}

	getCursorOffset := func(args map[string]interface{}, total int) (int, *ToolResult) {
		rawCursor, exists := args["cursor"]
		if !exists || rawCursor == nil {
			return 0, nil
		}

		var cursor int
		switch typed := rawCursor.(type) {
		case json.Number:
			parsed, err := strconv.ParseInt(typed.String(), 10, 64)
			if err != nil {
				return 0, &ToolResult{
					Content: []Content{{Type: "text", Text: "Argument 'cursor' must be an integer offset or string cursor"}},
					IsError: true,
				}
			}
			cursor = int(parsed)
		case float64:
			if typed != float64(int(typed)) {
				return 0, &ToolResult{
					Content: []Content{{Type: "text", Text: "Argument 'cursor' must be an integer offset or string cursor"}},
					IsError: true,
				}
			}
			cursor = int(typed)
		case int:
			cursor = typed
		case int32:
			cursor = int(typed)
		case int64:
			cursor = int(typed)
		case string:
			if strings.TrimSpace(typed) == "" {
				return 0, nil
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err != nil {
				return 0, &ToolResult{
					Content: []Content{{Type: "text", Text: "Argument 'cursor' must be an integer offset or string cursor"}},
					IsError: true,
				}
			}
			cursor = parsed
		default:
			return 0, &ToolResult{
				Content: []Content{{Type: "text", Text: "Argument 'cursor' must be an integer offset or string cursor"}},
				IsError: true,
			}
		}

		if cursor < 0 {
			return 0, &ToolResult{
				Content: []Content{{Type: "text", Text: "Argument 'cursor' must be zero or greater"}},
				IsError: true,
			}
		}
		if cursor > total {
			return 0, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Cursor %d is out of range for %d matching tools", cursor, total)}},
				IsError: true,
			}
		}

		return cursor, nil
	}

	getSortedGames := func(cfg *config.GamesConfig) []config.GameConfig {
		games := cfg.ListGames()
		sort.Slice(games, func(i, j int) bool {
			return games[i].ID < games[j].ID
		})
		return games
	}

	listToolsForDiscovery := func(cfg *config.GamesConfig, gameID string, hasGameID bool, forceInitialSync bool) ([]listedGameTool, *config.GameConfig, *ToolResult) {
		entries := make([]listedGameTool, 0)

		if hasGameID {
			game, exists := s.resolveGameId(cfg, gameID)
			if !exists {
				return nil, nil, &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games_list to see available games.", gameID)}},
					IsError: true,
				}
			}

			if forceInitialSync {
				if err := s.ensureGameToolsMirrored(game.ID, 10*time.Second); err != nil {
					return nil, game, &ToolResult{
						Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' is connected, but syncing GABP tools failed: %v", game.ID, err)}},
						StructuredContent: map[string]interface{}{
							"gameId": game.ID,
							"error":  err.Error(),
						},
						IsError: true,
					}
				}
			}

			for _, tool := range s.getGameSpecificTools(game.ID) {
				entries = append(entries, listedGameTool{
					GameID:        game.ID,
					Tool:          tool,
					CanonicalName: toolCanonicalName(tool),
					LocalName:     toolLocalName(game.ID, tool),
				})
			}

			return entries, game, nil
		}

		for _, game := range getSortedGames(cfg) {
			if forceInitialSync {
				if err := s.ensureGameToolsMirrored(game.ID, 10*time.Second); err != nil {
					s.log.Debugw("failed to sync GABP tools during discovery", "gameId", game.ID, "error", err)
				}
			}
			for _, tool := range s.getGameSpecificTools(game.ID) {
				entries = append(entries, listedGameTool{
					GameID:        game.ID,
					Tool:          tool,
					CanonicalName: toolCanonicalName(tool),
					LocalName:     toolLocalName(game.ID, tool),
				})
			}
		}

		return entries, nil, nil
	}

	filterListedTools := func(entries []listedGameTool, query, prefix string) []listedGameTool {
		if query == "" && prefix == "" {
			return entries
		}

		query = strings.ToLower(query)
		prefix = strings.ToLower(prefix)
		matchesQuery := func(value string) bool {
			value = strings.ToLower(value)
			if strings.ContainsAny(query, "./_- ") {
				return strings.Contains(value, query)
			}

			tokens := strings.FieldsFunc(value, func(r rune) bool {
				return (r < 'a' || r > 'z') && (r < '0' || r > '9')
			})
			for _, token := range tokens {
				if strings.HasPrefix(token, query) {
					return true
				}
			}
			return false
		}

		filtered := make([]listedGameTool, 0, len(entries))
		for _, entry := range entries {
			if query != "" {
				if !matchesQuery(entry.Tool.Name) &&
					!matchesQuery(entry.CanonicalName) &&
					!matchesQuery(entry.LocalName) {
					continue
				}
			}

			if prefix != "" {
				registered := strings.ToLower(entry.Tool.Name)
				canonical := strings.ToLower(entry.CanonicalName)
				local := strings.ToLower(entry.LocalName)
				if !strings.HasPrefix(registered, prefix) &&
					!strings.HasPrefix(canonical, prefix) &&
					!strings.HasPrefix(local, prefix) {
					continue
				}
			}

			filtered = append(filtered, entry)
		}

		return filtered
	}

	paginateListedTools := func(entries []listedGameTool, cursor, limit int) ([]listedGameTool, string) {
		if cursor >= len(entries) {
			return []listedGameTool{}, ""
		}
		if limit <= 0 {
			return entries[cursor:], ""
		}

		end := cursor + limit
		if end > len(entries) {
			end = len(entries)
		}

		nextCursor := ""
		if end < len(entries) {
			nextCursor = strconv.Itoa(end)
		}

		return entries[cursor:end], nextCursor
	}

	buildToolNameItemsWithOptions := func(entries []listedGameTool, brief bool) []map[string]interface{} {
		items := make([]map[string]interface{}, 0, len(entries))
		for _, entry := range entries {
			item := map[string]interface{}{
				"name":      entry.Tool.Name,
				"gameId":    entry.GameID,
				"localName": entry.LocalName,
			}
			if entry.CanonicalName != entry.Tool.Name {
				item["originalName"] = entry.CanonicalName
			}
			if gabpName := toolMetaString(entry.Tool, toolMetaGABPName); gabpName != "" {
				item["gabpName"] = gabpName
			}
			if tags := toolMetaStringSlice(entry.Tool, toolMetaTags); len(tags) > 0 {
				item["tags"] = tags
			}
			if brief {
				if summary := toolBriefDescription(entry.Tool.Description); summary != "" {
					item["summary"] = summary
				}
			}
			items = append(items, item)
		}
		return items
	}

	buildDetailedToolItems := func(entries []listedGameTool) []map[string]interface{} {
		items := make([]map[string]interface{}, 0, len(entries))
		for _, entry := range entries {
			item := map[string]interface{}{
				"name":         entry.Tool.Name,
				"gameId":       entry.GameID,
				"localName":    entry.LocalName,
				"description":  entry.Tool.Description,
				"inputSchema":  entry.Tool.InputSchema,
				"outputSchema": entry.Tool.OutputSchema,
			}
			if entry.CanonicalName != entry.Tool.Name {
				item["originalName"] = entry.CanonicalName
			}
			if gabpName := toolMetaString(entry.Tool, toolMetaGABPName); gabpName != "" {
				item["gabpName"] = gabpName
			}
			if tags := toolMetaStringSlice(entry.Tool, toolMetaTags); len(tags) > 0 {
				item["tags"] = tags
			}
			items = append(items, item)
		}
		return items
	}

	buildToolNotFoundResult := func(game *config.GameConfig, requested, message string, entries []listedGameTool) *ToolResult {
		candidates := filterListedTools(entries, requested, "")
		if len(candidates) > 10 {
			candidates = candidates[:10]
		}

		discoverArgs := map[string]interface{}{"brief": true}
		if game != nil {
			discoverArgs["gameId"] = game.ID
		}
		if strings.TrimSpace(requested) != "" {
			discoverArgs["query"] = requested
		}

		structured := map[string]interface{}{
			"requested":      requested,
			"availableTotal": len(entries),
			"candidates":     buildToolNameItemsWithOptions(candidates, true),
			"nextActions": []map[string]interface{}{
				mcpNextAction("games_tool_names", discoverArgs, "Discover available game tools before retrying."),
			},
		}
		if game != nil {
			structured["gameId"] = game.ID
		}

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: message}},
			StructuredContent: structured,
			IsError:           true,
		}
	}

	describeDiscoveryFilters := func(query, prefix string) string {
		parts := make([]string, 0, 2)
		if strings.TrimSpace(query) != "" {
			parts = append(parts, fmt.Sprintf("query %q", query))
		}
		if strings.TrimSpace(prefix) != "" {
			parts = append(parts, fmt.Sprintf("prefix %q", prefix))
		}

		return strings.Join(parts, " and ")
	}

	buildNoToolsMessage := func(game *config.GameConfig, noun string) string {
		var content strings.Builder
		if game != nil {
			content.WriteString(fmt.Sprintf("No game-specific %s available for '%s'.\n", noun, game.ID))
			status := s.checkGameStatus(game.ID)
			if status != "running" && status != "connected" {
				content.WriteString(fmt.Sprintf("Game is currently '%s'. Start it with games_start and connect with games_connect to enable GABP tools.\n", status))
			} else {
				content.WriteString("The game is running, but no GABP tools are currently connected.\n")
			}
			return content.String()
		}

		content.WriteString(fmt.Sprintf("No game-specific %s available.\n", noun))
		content.WriteString("Start games with GABP-compliant bridges to see their tools.\n")
		return content.String()
	}

	buildNoMatchingToolsMessage := func(game *config.GameConfig, noun string, availableTotal int, query, prefix string) string {
		var content strings.Builder
		content.WriteString(fmt.Sprintf("No matching game-specific %s", noun))
		if game != nil {
			content.WriteString(fmt.Sprintf(" for '%s'", game.ID))
		}
		content.WriteString(".\n")

		filterSummary := describeDiscoveryFilters(query, prefix)
		if game != nil {
			content.WriteString(fmt.Sprintf("%d game-specific tools are currently connected for this game", availableTotal))
		} else {
			content.WriteString(fmt.Sprintf("%d game-specific tools are currently connected across configured games", availableTotal))
		}
		if filterSummary != "" {
			content.WriteString(fmt.Sprintf(", but none matched %s.\n", filterSummary))
		} else {
			content.WriteString(", but none matched the requested filters.\n")
		}
		content.WriteString("Use games_tool_names without filters to browse compact names, then inspect one tool with games_tool_detail.\n")
		return content.String()
	}

	findListedTool := func(entries []listedGameTool, gameID, requested string) (listedGameTool, bool) {
		for _, entry := range entries {
			if toolMatchesRequestedName(gameID, entry.Tool, requested) {
				return entry, true
			}
		}
		return listedGameTool{}, false
	}

	resolveListedTool := func(gameID string, hasGameID bool, requested string, forceInitialSync bool) (listedGameTool, *ToolResult) {
		requested = strings.TrimSpace(requested)
		if requested == "" {
			return listedGameTool{}, &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: tool"}},
				IsError: true,
			}
		}

		if hasGameID {
			entries, game, listErr := listToolsForDiscovery(gamesConfig, gameID, true, forceInitialSync)
			if listErr != nil {
				return listedGameTool{}, listErr
			}

			entry, found := findListedTool(entries, game.ID, requested)
			if !found {
				message := fmt.Sprintf("Tool '%s' not found for game '%s'. Use games_tool_names to discover available names first.", requested, game.ID)
				return listedGameTool{}, buildToolNotFoundResult(game, requested, message, entries)
			}

			return entry, nil
		}

		entries, _, listErr := listToolsForDiscovery(gamesConfig, "", false, forceInitialSync)
		if listErr != nil {
			return listedGameTool{}, listErr
		}

		matches := make([]listedGameTool, 0, 1)
		for _, entry := range entries {
			if toolMatchesRequestedName(entry.GameID, entry.Tool, requested) {
				matches = append(matches, entry)
			}
		}

		switch len(matches) {
		case 0:
			message := fmt.Sprintf("Tool '%s' was not found. Use games_tool_names to discover available names first, or include gameId if you are using a local tool name.", requested)
			return listedGameTool{}, buildToolNotFoundResult(nil, requested, message, entries)
		case 1:
			return matches[0], nil
		default:
			gameIDs := make([]string, 0, len(matches))
			seen := make(map[string]struct{}, len(matches))
			for _, entry := range matches {
				if _, exists := seen[entry.GameID]; exists {
					continue
				}
				seen[entry.GameID] = struct{}{}
				gameIDs = append(gameIDs, entry.GameID)
			}
			sort.Strings(gameIDs)
			return listedGameTool{}, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Tool '%s' matched multiple games (%s). Include gameId or use the fully qualified mirrored tool name.", requested, strings.Join(gameIDs, ", "))}},
				IsError: true,
			}
		}
	}

	// games_tool_names tool - Compact game tool discovery for AI clients
	s.RegisterToolWithConfig(Tool{
		Name:        "games.tool_names",
		Description: "List compact game-specific tool names. Use this first for low-token discovery, then call games_tool_detail for one tool.",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to list tools for (optional, lists all configured games if not provided)",
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Case-insensitive text filter applied to tool names (optional)",
				},
				"prefix": map[string]interface{}{
					"type":        "string",
					"description": "Prefix filter applied to the full tool name and local name (optional)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of names to return (optional, defaults to 50)",
				},
				"cursor": map[string]interface{}{
					"type":        "string",
					"description": "Offset cursor returned by a previous page (optional)",
				},
				"brief": map[string]interface{}{
					"type":        "boolean",
					"description": "Include a one-line summary per tool in structured output only (optional, default false)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "brief", "cursor", "gameId", "limit", "prefix", "query"); res != nil {
			return res, nil
		}
		gamesConfig, _, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameID, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}
		query, _, invalidArg := getOptionalStringArg(args, "query")
		if invalidArg != nil {
			return invalidArg, nil
		}
		prefix, _, invalidArg := getOptionalStringArg(args, "prefix")
		if invalidArg != nil {
			return invalidArg, nil
		}
		brief, _, invalidArg := getOptionalBoolArg(args, "brief")
		if invalidArg != nil {
			return invalidArg, nil
		}

		entries, game, listErr := listToolsForDiscovery(gamesConfig, gameID, hasGameID, true)
		if listErr != nil {
			return listErr, nil
		}

		availableTotal := len(entries)
		entries = filterListedTools(entries, query, prefix)
		total := len(entries)

		limit, hasLimit, invalidArg := getOptionalPositiveIntArg(args, "limit")
		if invalidArg != nil {
			return invalidArg, nil
		}
		if !hasLimit {
			limit = 50
		}

		cursor, invalidCursor := getCursorOffset(args, total)
		if invalidCursor != nil {
			return invalidCursor, nil
		}

		page, nextCursor := paginateListedTools(entries, cursor, limit)
		if len(page) == 0 {
			message := buildNoToolsMessage(game, "tool names")
			if total > 0 && cursor >= total {
				message = fmt.Sprintf("No more matching tool names for cursor %d.\nStart again without a cursor or use a smaller cursor.\n", cursor)
			} else if availableTotal > 0 && (query != "" || prefix != "") {
				message = buildNoMatchingToolsMessage(game, "tool names", availableTotal, query, prefix)
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: message}},
				StructuredContent: map[string]interface{}{
					"availableTotal": availableTotal,
					"gameId":         gameID,
					"total":          total,
					"returned":       0,
					"nextCursor":     nextCursor,
					"tools":          buildToolNameItemsWithOptions(nil, brief),
				},
			}, nil
		}

		var content strings.Builder
		scope := "all games"
		if game != nil {
			scope = fmt.Sprintf("game '%s'", game.ID)
		}
		content.WriteString(fmt.Sprintf("Tool names for %s (%d shown of %d matching):\n", scope, len(page), total))
		for _, entry := range page {
			content.WriteString(entry.Tool.Name)
			content.WriteString("\n")
		}
		content.WriteString("\nUse games_tool_detail with one of these names to inspect parameters and output. Legacy alias: games.tool_detail.")
		if nextCursor != "" {
			content.WriteString(fmt.Sprintf("\nNext cursor: %s", nextCursor))
		}

		structured := map[string]interface{}{
			"availableTotal": availableTotal,
			"total":          total,
			"returned":       len(page),
			"tools":          buildToolNameItemsWithOptions(page, brief),
			"nextCursor":     nextCursor,
		}
		if game != nil {
			structured["gameId"] = game.ID
		}
		if query != "" {
			structured["query"] = query
		}
		if prefix != "" {
			structured["prefix"] = prefix
		}
		if brief {
			structured["brief"] = true
		}

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: strings.TrimRight(content.String(), "\n")}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games_tool_detail tool - Detailed schema for one discovered tool
	s.RegisterToolWithConfig(Tool{
		Name:        "games.tool_detail",
		Description: "Show detailed metadata for one game-specific tool, including parameters and output schema.",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to inspect the tool in (optional if the tool name is fully qualified or uniquely discoverable)",
				},
				"tool": map[string]interface{}{
					"type":        "string",
					"description": "Tool name as returned by games_tool_names or games_tools (required). Prefer the fully qualified mirrored name, e.g. 'example.core.ping'.",
				},
			},
			"required": []string{"tool"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "gameId", "tool"); res != nil {
			return res, nil
		}
		gamesConfig, _, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameID, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}
		requestedTool, ok := args["tool"].(string)
		if !ok || strings.TrimSpace(requestedTool) == "" {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: tool"}},
				IsError: true,
			}, nil
		}

		entry, resolveErr := resolveListedTool(gameID, hasGameID, requestedTool, true)
		if resolveErr != nil {
			return resolveErr, nil
		}

		var content strings.Builder
		content.WriteString(fmt.Sprintf("Tool detail for '%s' in game '%s'\n", entry.Tool.Name, entry.GameID))
		if entry.Tool.Description != "" {
			content.WriteString("\n")
			content.WriteString(entry.Tool.Description)
		}
		if entry.CanonicalName != entry.Tool.Name {
			content.WriteString(fmt.Sprintf("\n\nOriginal name: %s", entry.CanonicalName))
		}
		writeToolParams(&content, entry.Tool)

		structured := map[string]interface{}{
			"gameId":       entry.GameID,
			"name":         entry.Tool.Name,
			"localName":    entry.LocalName,
			"description":  entry.Tool.Description,
			"inputSchema":  entry.Tool.InputSchema,
			"outputSchema": entry.Tool.OutputSchema,
		}
		if entry.CanonicalName != entry.Tool.Name {
			structured["originalName"] = entry.CanonicalName
		}
		if gabpName := toolMetaString(entry.Tool, toolMetaGABPName); gabpName != "" {
			structured["gabpName"] = gabpName
		}
		if tags := toolMetaStringSlice(entry.Tool, toolMetaTags); len(tags) > 0 {
			structured["tags"] = tags
		}

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: strings.TrimSpace(content.String())}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games_tools tool - Detailed tool listing, kept for compatibility
	s.RegisterToolWithConfig(Tool{
		Name:        "games.tools",
		Description: "List game-specific tools in detailed form for compatibility and human-readable inspection. Prefer games_tool_names for compact discovery and games_tool_detail for one tool.",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to list tools for (optional, lists all if not provided)",
				},
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Case-insensitive text filter applied to tool names (optional)",
				},
				"prefix": map[string]interface{}{
					"type":        "string",
					"description": "Prefix filter applied to the full tool name and local name (optional)",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of tools to return (optional)",
				},
				"cursor": map[string]interface{}{
					"type":        "string",
					"description": "Offset cursor returned by a previous page (optional)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "cursor", "gameId", "limit", "prefix", "query"); res != nil {
			return res, nil
		}
		gamesConfig, _, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameID, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}
		query, _, invalidArg := getOptionalStringArg(args, "query")
		if invalidArg != nil {
			return invalidArg, nil
		}
		prefix, _, invalidArg := getOptionalStringArg(args, "prefix")
		if invalidArg != nil {
			return invalidArg, nil
		}

		entries, game, listErr := listToolsForDiscovery(gamesConfig, gameID, hasGameID, true)
		if listErr != nil {
			return listErr, nil
		}

		availableTotal := len(entries)
		entries = filterListedTools(entries, query, prefix)
		total := len(entries)

		limit, _, invalidArg := getOptionalPositiveIntArg(args, "limit")
		if invalidArg != nil {
			return invalidArg, nil
		}
		cursor, invalidCursor := getCursorOffset(args, total)
		if invalidCursor != nil {
			return invalidCursor, nil
		}

		page, nextCursor := paginateListedTools(entries, cursor, limit)
		if len(page) == 0 {
			message := buildNoToolsMessage(game, "tools")
			if total > 0 && cursor >= total {
				message = fmt.Sprintf("No more matching tools for cursor %d.\nStart again without a cursor or use a smaller cursor.\n", cursor)
			} else if availableTotal > 0 && (query != "" || prefix != "") {
				message = buildNoMatchingToolsMessage(game, "tools", availableTotal, query, prefix)
			}

			return &ToolResult{
				Content: []Content{{Type: "text", Text: message}},
				StructuredContent: map[string]interface{}{
					"availableTotal": availableTotal,
					"gameId":         gameID,
					"total":          total,
					"returned":       0,
					"nextCursor":     nextCursor,
					"tools":          buildDetailedToolItems(nil),
				},
			}, nil
		}

		var content strings.Builder
		if game != nil {
			content.WriteString(fmt.Sprintf("Tools for game '%s' (%d shown of %d matching):\n\n", game.ID, len(page), total))
			for _, entry := range page {
				content.WriteString(fmt.Sprintf("• **%s** - %s", entry.Tool.Name, entry.Tool.Description))
				writeToolParams(&content, entry.Tool)
				content.WriteString("\n")
			}
		} else {
			content.WriteString(fmt.Sprintf("Game-Specific Tools Available (%d shown of %d matching):\n\n", len(page), total))
			currentGameID := ""
			for _, entry := range page {
				if entry.GameID != currentGameID {
					if currentGameID != "" {
						content.WriteString("\n")
					}
					currentGameID = entry.GameID
					status := s.checkGameStatus(entry.GameID)
					content.WriteString(fmt.Sprintf("**%s** (%s):\n", entry.GameID, status))
				}
				content.WriteString(fmt.Sprintf("  • %s - %s", entry.Tool.Name, entry.Tool.Description))
				writeToolParams(&content, entry.Tool)
				content.WriteString("\n")
			}
			content.WriteString("\nNote: Tools are prefixed with game ID (e.g., 'factory.inventory.get') to avoid conflicts between games.\n")
		}

		content.WriteString("\nUse games_tool_names for a smaller list and games_tool_detail for one tool.")
		if nextCursor != "" {
			content.WriteString(fmt.Sprintf("\nNext cursor: %s", nextCursor))
		}

		structured := map[string]interface{}{
			"availableTotal": availableTotal,
			"total":          total,
			"returned":       len(page),
			"tools":          buildDetailedToolItems(page),
			"nextCursor":     nextCursor,
		}
		if game != nil {
			structured["gameId"] = game.ID
		}
		if query != "" {
			structured["query"] = query
		}
		if prefix != "" {
			structured["prefix"] = prefix
		}

		return &ToolResult{
			Content:           []Content{{Type: "text", Text: strings.TrimRight(content.String(), "\n")}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games_connect tool - Manually connect to a game's GABP server
	s.RegisterToolWithConfig(Tool{
		Name:        "games.connect",
		Description: "Connect to a running game's GABP server to discover and sync tools. Use this after the game has fully loaded.",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to connect to (required)",
				},
				"forceTakeover": map[string]interface{}{
					"type":        "boolean",
					"description": "When true, override another live GABS session's ownership record and attempt to connect anyway. Defaults to false.",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Request timeout in seconds (optional, default 15). Increase for slow game loads or slow tool discovery.",
				},
			},
			"required": []string{"gameId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "forceTakeover", "gameId", "timeout"); res != nil {
			return res, nil
		}
		gamesConfig, configRevision, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameIdArg, ok := args["gameId"].(string)
		if !ok || gameIdArg == "" {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: gameId"}},
				IsError: true,
			}, nil
		}

		game, resolveFail := resolveGameResult(gamesConfig, gameIdArg)
		if resolveFail != nil {
			return resolveFail, nil
		}

		forceTakeover, _, forceTakeoverErr := getOptionalBoolArg(args, "forceTakeover")
		if forceTakeoverErr != nil {
			return forceTakeoverErr, nil
		}
		connectTimeout, invalidTimeout := parseOptionalTimeoutSecondsArg(args, "timeout", 15*time.Second)
		if invalidTimeout != nil {
			return invalidTimeout, nil
		}

		runtimeState, runtimeErr := process.LoadRuntimeState(game.ID, s.configDir)
		if runtimeErr != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to inspect shared runtime state for '%s': %v", game.ID, runtimeErr)}},
				IsError: true,
			}, nil
		}

		// Already connected: only a claim-BOUND live client counts (review
		// round 8) — a lingering client for an earlier launch can neither
		// satisfy a connect nor be attributed to the current claim. Unbound
		// clients are closed so the fresh attempt owns the slot cleanly.
		if boundClient, boundClaim := s.claimBoundClient(game.ID); boundClient != nil &&
			runtimeState != nil && boundClaim.LaunchID == runtimeState.LaunchID {
			if err := s.syncGABPToolsWithTimeout(boundClient, game.ID, connectTimeout); err != nil {
				return &ToolResult{
					Content: []Content{{Type: "text", Text: fmt.Sprintf("Already connected to '%s' but failed to sync tools: %v", game.ID, err)}},
					IsError: true,
				}, nil
			}
			toolCount := len(s.getGameSpecificTools(game.ID))
			structured := map[string]interface{}{
				"gameId":    game.ID,
				"connected": true,
				"toolCount": toolCount,
				"nextActions": []map[string]interface{}{
					mcpNextAction("games_tool_names", map[string]interface{}{"gameId": game.ID, "brief": true}, "Discover connected game-specific tools."),
				},
			}
			s.attachClaimIdentity(structured, game.ID)
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Already connected to '%s'. Re-synced %d tools.", game.ID, toolCount)}},
				StructuredContent: structured,
			}, nil
		}
		s.mu.RLock()
		_, hasLingeringClient := s.gabpClients[game.ID]
		s.mu.RUnlock()
		if hasLingeringClient {
			s.CleanupGABPConnection(game.ID)
		}
		hadForeignOwner := process.RuntimeStateOwnedByAnotherLiveOwner(runtimeState, os.Getpid(), s.instanceID)
		foreignOwnerActive := process.RuntimeStateOwnedByAnotherActiveOwner(runtimeState, os.Getpid(), s.instanceID, s.runtimeOwnerLeaseDuration(), time.Now().UTC())
		previousOwnerPID := 0
		previousOwnerInstance := ""
		if runtimeState != nil {
			previousOwnerPID = runtimeState.OwnerPID
			previousOwnerInstance = runtimeState.OwnerInstanceID
		}
		if foreignOwnerActive && !forceTakeover {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' is already owned by another active GABS session (pid %d). Skipping games_connect here to avoid competing bridge clients.", game.ID, runtimeState.OwnerPID)}},
				StructuredContent: map[string]interface{}{
					"gameId":        game.ID,
					"foreignOwner":  true,
					"ownerActive":   true,
					"ownerPID":      runtimeState.OwnerPID,
					"ownerInstance": runtimeState.OwnerInstanceID,
					"nextActions": []map[string]interface{}{
						mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Check current ownership and connection state."),
						mcpNextAction("games_connect", map[string]interface{}{"gameId": game.ID}, "Retry after the active owner lease expires."),
						mcpNextAction("games_connect", map[string]interface{}{"gameId": game.ID, "forceTakeover": true}, "Use only when intentionally moving control to this GABS session immediately."),
					},
				},
			}, nil
		}
		idleTakeover := hadForeignOwner && !foreignOwnerActive && !forceTakeover

		// games_connect is a lifecycle touch — but only once it actually
		// proceeds: a connect refused by the ownership gate above must not
		// normalize the claim or burn the one-shot migration candidate.
		var legacyEndpointCandidate *process.RuntimeEndpoint
		if runtimeState != nil && runtimeState.SchemaVersion == 0 {
			// The marker-absent claim normalizes fully before anything acts
			// on it, and the one legacy bridge.json candidate is captured
			// under that same transition lock (design/07: the sole
			// live-attach read of the file, exactly once) — validated below
			// by actually connecting, then persisted through the minted
			// launch fence. Never reread.
			normalized, candidate, nerr := process.NormalizeLegacyClaimCapturingEndpoint(game.ID, s.configDir, game.LaunchMode, configRevision)
			if nerr != nil {
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("The pre-upgrade runtime claim for '%s' could not be normalized: %v", game.ID, nerr)}},
					IsError:           true,
					StructuredContent: map[string]interface{}{"code": "blocked_unknown_state", "gameId": game.ID},
				}, nil
			}
			runtimeState = normalized
			legacyEndpointCandidate = candidate
		}

		status := s.checkGameStatus(game.ID)

		var endpoint bridgeEndpoint
		if legacyEndpointCandidate != nil {
			endpoint = bridgeEndpoint{Port: legacyEndpointCandidate.Port, Token: legacyEndpointCandidate.Token, Source: "legacy-bridge-file"}
		} else {
			var eerr error
			endpoint, eerr = s.resolveConnectBridgeEndpoint(*game, runtimeState)
			if eerr != nil {
				var stale *errStaleConnectCredential
				if errors.As(eerr, &stale) {
					return staleBridgeCredentialResult(game.ID, stale.Error()), nil
				}
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("Failed to resolve live GABP endpoint for '%s': %v", game.ID, eerr)}},
					IsError:           true,
					StructuredContent: map[string]interface{}{"code": "endpoint_unavailable", "gameId": game.ID},
				}, nil
			}
		}
		port := endpoint.Port
		token := endpoint.Token

		var runtimeStateBeforeClaim *process.RuntimeState
		if runtimeState != nil {
			stateCopy := *runtimeState
			runtimeStateBeforeClaim = &stateCopy
		}
		runtimeState, err := s.saveRuntimeOwnerLease(*game, runtimeState, connectTimeout)
		if err != nil {
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Failed to claim runtime ownership for '%s': %v", game.ID, err)}},
				IsError:           true,
				StructuredContent: map[string]interface{}{"code": "blocked_unknown_state", "gameId": game.ID},
			}, nil
		}

		// Reattach with the claim's own credential (or the captured legacy
		// migration candidate) — never a substituted one. The migration
		// attempt authenticates only: publication and mirroring follow the
		// fenced endpoint persist, in that order (design/07; round 8).
		var connector *ServerGABPConnector
		if legacyEndpointCandidate != nil {
			connector = NewLegacyMigrationConnector(s, backoffMin, backoffMax)
		} else {
			connector = NewServerGABPConnector(s, backoffMin, backoffMax)
		}
		connectCtx, connectCancel := context.WithTimeout(context.Background(), connectTimeout)
		defer connectCancel()

		err = connector.AttemptConnection(connectCtx, game.ID, port, token)
		if err != nil {
			var staleConn *staleBridgeCredentialError
			if errors.As(err, &staleConn) {
				s.restoreRuntimeOwnerAfterFailedConnect(game.ID, runtimeStateBeforeClaim)
				return staleBridgeCredentialResult(game.ID, staleConn.Error()), nil
			}
			var superseded *supersededConnectionError
			if errors.As(err, &superseded) {
				s.restoreRuntimeOwnerAfterFailedConnect(game.ID, runtimeStateBeforeClaim)
				return &ToolResult{
					Content: []Content{{Type: "text", Text: superseded.Error()}},
					StructuredContent: map[string]interface{}{
						"code":   "operation_in_progress",
						"gameId": game.ID,
						"nextActions": []map[string]interface{}{
							mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "The launch changed while connecting; inspect the current state."),
						},
					},
					IsError: true,
				}, nil
			}
			disconnectNote := s.describeLastGABPDisconnect(game.ID)
			if status != "running" && status != "connected" {
				if status == "running-disconnected" {
					status = "running"
				}
				if disconnectNote != "" {
					disconnectNote = "\n" + disconnectNote
				}
				s.restoreRuntimeOwnerAfterFailedConnect(game.ID, runtimeStateBeforeClaim)
				return &ToolResult{
					Content:           []Content{{Type: "text", Text: fmt.Sprintf("Failed to connect to GABP server for '%s' on port %d after %s: %v. GABS currently sees status '%s'. Make sure the game is still running and the GABP bridge is fully loaded.%s", game.ID, port, connectTimeout.Round(time.Second), err, status, disconnectNote)}},
					IsError:           true,
					StructuredContent: map[string]interface{}{"code": "blocked_unknown_state", "gameId": game.ID},
				}, nil
			}

			if disconnectNote != "" {
				disconnectNote = "\n" + disconnectNote
			}
			s.restoreRuntimeOwnerAfterFailedConnect(game.ID, runtimeStateBeforeClaim)
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Failed to connect to GABP server for '%s' on port %d after %s: %v. Make sure the GABP bridge is loaded.%s", game.ID, port, connectTimeout.Round(time.Second), err, disconnectNote)}},
				IsError:           true,
				StructuredContent: map[string]interface{}{"code": "blocked_unknown_state", "gameId": game.ID},
			}, nil
		}

		if legacyEndpointCandidate != nil {
			// The pre-upgrade migration (design/07): persist the endpoint
			// this live connection just validated through the minted launch
			// fence, publish the attachment, and only then expose tools. Any
			// failure is terminal — close the exact client, restore only the
			// ownership fields, and report a structured failure; a legacy
			// bridge is never exposed under a successor claim (round 8).
			s.mu.RLock()
			migratedClient := s.gabpClients[game.ID]
			s.mu.RUnlock()
			failMigration := func(code, text string) (*ToolResult, error) {
				if migratedClient != nil {
					s.mu.Lock()
					if cur, exists := s.gabpClients[game.ID]; exists && cur == migratedClient {
						delete(s.gabpClients, game.ID)
					}
					s.mu.Unlock()
					_ = migratedClient.Close()
				}
				s.restoreRuntimeOwnerAfterFailedConnect(game.ID, runtimeStateBeforeClaim)
				return &ToolResult{
					Content: []Content{{Type: "text", Text: text}},
					StructuredContent: map[string]interface{}{
						"code":   code,
						"gameId": game.ID,
						"nextActions": []map[string]interface{}{
							mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Inspect the current launch state."),
						},
					},
					IsError: true,
				}, nil
			}
			if _, merr := process.FencedTransition(game.ID, s.configDir, runtimeState.LaunchID, "", func(st *process.RuntimeState) error {
				if st.Endpoint != nil {
					return process.ErrFencingViolation // already migrated meanwhile
				}
				st.Endpoint = &process.RuntimeEndpoint{Port: port, Token: token}
				return nil
			}); merr != nil {
				if errors.Is(merr, process.ErrFencingViolation) || errors.Is(merr, process.ErrNoRuntimeClaim) {
					return failMigration("operation_in_progress", fmt.Sprintf("The pre-upgrade claim for '%s' was replaced while its endpoint was being validated; the connection was closed — re-check games_status.", game.ID))
				}
				return failMigration("endpoint_unavailable", fmt.Sprintf("The validated legacy endpoint for '%s' could not be persisted: %v. The connection was closed; retry games_connect is not possible for this launch — the migration window is one-shot.", game.ID, merr))
			}
			migratedRef, perr := s.recordBridgeAttachment(game.ID, migratedClient, port, token, func() bool {
				s.mu.RLock()
				cur := s.gabpClients[game.ID]
				s.mu.RUnlock()
				return cur == migratedClient && migratedClient != nil && migratedClient.IsConnected()
			})
			if perr != nil {
				return failMigration("operation_in_progress", fmt.Sprintf("The migrated bridge attachment for '%s' could not be published (%v); the connection was closed — re-check games_status.", game.ID, perr))
			}
			if migratedClient != nil {
				s.recordContextDelivery(game.ID, migratedRef, migratedClient.TakeObservedContext())
			}
			if migratedClient != nil {
				if merr := connector.MirrorConnectedClient(connectCtx, game.ID, migratedClient); merr != nil {
					s.HandleUnexpectedGABPDisconnect(game.ID, migratedClient, merr)
					s.restoreRuntimeOwnerAfterFailedConnect(game.ID, runtimeStateBeforeClaim)
					return &ToolResult{
						Content: []Content{{Type: "text", Text: fmt.Sprintf("Connected and migrated '%s', but tool mirroring failed: %v", game.ID, merr)}},
						IsError: true,
					}, nil
				}
			}
		}

		toolCount := len(s.getGameSpecificTools(game.ID))

		if _, err := s.saveRuntimeOwnerLease(*game, runtimeState, 0); err != nil {
			s.log.Warnw("failed to persist runtime ownership after connect", "gameId", game.ID, "error", err)
		}

		if hadForeignOwner && forceTakeover {
			structured := map[string]interface{}{
				"gameId":        game.ID,
				"connected":     true,
				"forceTakeover": true,
				"previousPID":   previousOwnerPID,
				"previousOwner": previousOwnerInstance,
				"port":          port,
				"toolCount":     toolCount,
				"nextActions": []map[string]interface{}{
					mcpNextAction("games_tool_names", map[string]interface{}{"gameId": game.ID, "brief": true}, "Discover connected game-specific tools."),
				},
			}
			s.attachClaimIdentity(structured, game.ID)
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Force-took ownership of '%s' from GABS pid %d and connected to the GABP server on port %d. Discovered %d tools.", game.ID, previousOwnerPID, port, toolCount)}},
				StructuredContent: structured,
			}, nil
		}

		if idleTakeover {
			structured := map[string]interface{}{
				"gameId":        game.ID,
				"connected":     true,
				"idleTakeover":  true,
				"previousPID":   previousOwnerPID,
				"previousOwner": previousOwnerInstance,
				"port":          port,
				"toolCount":     toolCount,
				"nextActions": []map[string]interface{}{
					mcpNextAction("games_tool_names", map[string]interface{}{"gameId": game.ID, "brief": true}, "Discover connected game-specific tools."),
				},
			}
			s.attachClaimIdentity(structured, game.ID)
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Took ownership of idle GABS session for '%s' from pid %d and connected to the GABP server on port %d. Discovered %d tools.", game.ID, previousOwnerPID, port, toolCount)}},
				StructuredContent: structured,
			}, nil
		}

		structured := map[string]interface{}{
			"gameId":    game.ID,
			"connected": true,
			"port":      port,
			"toolCount": toolCount,
			"nextActions": []map[string]interface{}{
				mcpNextAction("games_tool_names", map[string]interface{}{"gameId": game.ID, "brief": true}, "Discover connected game-specific tools."),
			},
		}
		s.attachClaimIdentity(structured, game.ID)
		return &ToolResult{
			Content:           []Content{{Type: "text", Text: fmt.Sprintf("Successfully connected to '%s' GABP server on port %d. Discovered %d tools.", game.ID, port, toolCount)}},
			StructuredContent: structured,
		}, nil
	}, normalizationConfig)

	// games_get_attention - Inspect the current attention state for a connected game
	s.RegisterToolWithConfig(Tool{
		Name:        "games.get_attention",
		Description: "Inspect the current attention item for a connected game. Use this when GABS blocks further game calls due to important async game information.",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to inspect (optional if exactly one game is connected via GABP)",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Request timeout in seconds when refreshing the current attention state from the game (optional, default 10)",
				},
			},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "gameId", "timeout"); res != nil {
			return res, nil
		}
		gamesConfig, _, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameID, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}

		timeout, invalidTimeout := parseOptionalTimeoutSecondsArg(args, "timeout", 10*time.Second)
		if invalidTimeout != nil {
			return invalidTimeout, nil
		}

		game, client, resolveErr := s.resolveAttentionClient(gamesConfig, gameID, hasGameID)
		if resolveErr != nil {
			return resolveErr, nil
		}

		if blocked := s.ensureRuntimeOwnershipForGameCall(game.ID, "games_get_attention", timeout); blocked != nil {
			return blocked, nil
		}

		if !client.SupportsAttention() {
			s.setGameAttentionSupport(game.ID, false)
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' does not advertise attention support yet.", game.ID)}},
				StructuredContent: map[string]interface{}{
					"gameId":    game.ID,
					"supported": false,
					"attention": nil,
				},
			}, nil
		}

		current, err := s.refreshCurrentAttention(game.ID, client, timeout)
		if err != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to query current attention for game '%s': %v", game.ID, err)}},
				IsError: true,
			}, nil
		}

		if current == nil || strings.EqualFold(current.State, "cleared") {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' has no open attention item.", game.ID)}},
				StructuredContent: map[string]interface{}{
					"gameId":    game.ID,
					"supported": true,
					"attention": nil,
					"blocking":  false,
				},
			}, nil
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' has an open %s attention item '%s'. %s", game.ID, current.Severity, current.AttentionID, current.Summary)}},
			StructuredContent: map[string]interface{}{
				"gameId":    game.ID,
				"supported": true,
				"attention": current,
				"blocking":  current.Blocking,
			},
		}, nil
	}, normalizationConfig)

	// games_ack_attention - Acknowledge the current blocking attention item for a connected game
	s.RegisterToolWithConfig(Tool{
		Name:        "games.ack_attention",
		Description: "Acknowledge a current attention item for a connected game so blocked game calls can resume once the game has no remaining blocking attention.",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to acknowledge attention for (optional if exactly one game is connected via GABP)",
				},
				"attentionId": map[string]interface{}{
					"type":        "string",
					"description": "Attention item identifier returned by games_get_attention or a blocked tool result",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Request timeout in seconds for the acknowledgement request (optional, default 10)",
				},
			},
			"required": []string{"attentionId"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "attentionId", "gameId", "timeout"); res != nil {
			return res, nil
		}
		gamesConfig, _, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameID, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}

		attentionID, ok := args["attentionId"].(string)
		if !ok || strings.TrimSpace(attentionID) == "" {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: attentionId"}},
				IsError: true,
			}, nil
		}

		timeout, invalidTimeout := parseOptionalTimeoutSecondsArg(args, "timeout", 10*time.Second)
		if invalidTimeout != nil {
			return invalidTimeout, nil
		}

		game, client, resolveErr := s.resolveAttentionClient(gamesConfig, gameID, hasGameID)
		if resolveErr != nil {
			return resolveErr, nil
		}

		if blocked := s.ensureRuntimeOwnershipForGameCall(game.ID, "games_ack_attention", timeout); blocked != nil {
			return blocked, nil
		}

		if !client.SupportsAttention() {
			s.setGameAttentionSupport(game.ID, false)
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' does not advertise attention support yet.", game.ID)}},
				StructuredContent: map[string]interface{}{
					"gameId":       game.ID,
					"supported":    false,
					"acknowledged": false,
					"attentionId":  attentionID,
				},
			}, nil
		}

		result, err := client.AcknowledgeAttentionWithTimeout(attentionID, timeout)
		if err != nil {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Failed to acknowledge attention '%s' for game '%s': %v", attentionID, game.ID, err)}},
				IsError: true,
			}, nil
		}

		s.setGameAttentionCurrent(game.ID, result.CurrentAttention)

		message := fmt.Sprintf("Attention '%s' was not acknowledged for game '%s'.", attentionID, game.ID)
		if result.Acknowledged {
			message = fmt.Sprintf("Acknowledged attention '%s' for game '%s'.", attentionID, game.ID)
		}
		if result.CurrentAttention != nil && strings.TrimSpace(result.CurrentAttention.Summary) != "" {
			message = fmt.Sprintf("%s Current attention: %s", strings.TrimRight(message, "."), result.CurrentAttention.Summary)
		}

		return &ToolResult{
			Content: []Content{{Type: "text", Text: message}},
			StructuredContent: map[string]interface{}{
				"gameId":           game.ID,
				"supported":        true,
				"acknowledged":     result.Acknowledged,
				"attentionId":      result.AttentionID,
				"currentAttention": result.CurrentAttention,
			},
			IsError: !result.Acknowledged,
		}, nil
	}, normalizationConfig)

	// games_call_tool - Proxy tool calls to a game's GABP server
	s.RegisterToolWithConfig(Tool{
		Name:        "games.call_tool",
		Description: "Call a game-specific tool on a running game via its GABP connection. Prefer games_tool_names for discovery and games_tool_detail for schema inspection before calling.",
		InputSchema: map[string]interface{}{
			"additionalProperties": false,
			"type":                 "object",
			"properties": map[string]interface{}{
				"gameId": map[string]interface{}{
					"type":        "string",
					"description": "Game ID to call the tool on (optional if the tool name is fully qualified or uniquely discoverable)",
				},
				"tool": map[string]interface{}{
					"type":        "string",
					"description": "Tool name as returned by games_tool_names or games_tools (required). Prefer the full mirrored name, e.g. 'example.core.ping'.",
				},
				"arguments": map[string]interface{}{
					"type":        "object",
					"description": "Arguments to pass to the tool (optional, depends on tool)",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "Request timeout in seconds (optional, default 30). Increase for long-running tools such as composite load-ready flows or screen-wait operations.",
				},
			},
			"required": []string{"tool"},
		},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		if res := strictArgs(args, "arguments", "gameId", "timeout", "tool"); res != nil {
			return res, nil
		}
		gamesConfig, _, cfgErr := s.currentGamesConfig()
		if gamesConfig == nil {
			return configUnavailableResult(cfgErr), nil
		}
		gameIdArg, hasGameID, invalidArg := getOptionalStringArg(args, "gameId")
		if invalidArg != nil {
			return invalidArg, nil
		}
		toolName, ok := args["tool"].(string)
		if !ok || toolName == "" {
			return &ToolResult{
				Content: []Content{{Type: "text", Text: "Missing required argument: tool"}},
				IsError: true,
			}, nil
		}

		toolArgs, _ := args["arguments"].(map[string]interface{})
		if toolArgs == nil {
			toolArgs = map[string]interface{}{}
		}

		timeout, invalidTimeout := parseOptionalTimeoutSecondsArg(args, "timeout", 30*time.Second)
		if invalidTimeout != nil {
			return invalidTimeout, nil
		}

		proxyTimeout, invalidProxyTimeout := deriveMirroredToolCallTimeout(toolArgs, timeout)
		if invalidProxyTimeout != nil {
			return invalidProxyTimeout, nil
		}

		entry, resolveErr := resolveListedTool(gameIdArg, hasGameID, toolName, false)
		if resolveErr != nil {
			if directResult, handled := s.callDirectGABPTool(gamesConfig, gameIdArg, hasGameID, toolName, toolArgs, proxyTimeout); handled {
				return directResult, nil
			}
			return resolveErr, nil
		}

		// Get the GABP client for this game — claim-bound only (round 8):
		// a live client for an earlier launch must not service tools under
		// the current claim's ownership.
		client, _ := s.claimBoundClient(entry.GameID)

		if client == nil {
			disconnectNote := s.describeLastGABPDisconnect(entry.GameID)
			if disconnectNote != "" {
				disconnectNote = " " + disconnectNote
			}
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' is not connected via GABP. Use games_status to verify whether it is still running, then use games_connect or games_start as appropriate.%s", entry.GameID, disconnectNote)}},
				IsError: true,
			}, nil
		}

		if blocked := s.ensureRuntimeOwnershipForGameCall(entry.GameID, fmt.Sprintf("tool '%s'", toolName), proxyTimeout); blocked != nil {
			return blocked, nil
		}

		// Resolve the requested name against the mirrored tools for this game.
		// This accepts the registered MCP name, the original dotted name, or the
		// local tool name, so games_call_tool keeps working under OpenAI tool name
		// normalization as well.
		gabpToolName := gabpToolNameFromTool(entry.GameID, entry.Tool)

		if !shouldBypassAttentionGateForTool(entry.Tool, toolName, gabpToolName, entry.Tool.Name) {
			if blocked := s.enforceAttentionGate(entry.GameID, entry.Tool.Name, client); blocked != nil {
				return blocked, nil
			}
		}

		result, isError, err := client.CallToolWithTimeout(gabpToolName, toolArgs, proxyTimeout)
		if err != nil {
			disconnectNote := s.describeLastGABPDisconnect(entry.GameID)
			if disconnectNote != "" {
				disconnectNote = " " + disconnectNote
			}
			return &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("GABP tool call failed: %v.%s", err, disconnectNote)}},
				IsError: true,
			}, nil
		}

		if isError {
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Tool error: %v", result)}},
				StructuredContent: result,
				IsError:           true,
			}, nil
		}

		// Convert result to text content
		content := []Content{}
		if resultText, ok := result["text"].(string); ok {
			content = append(content, Content{Type: "text", Text: resultText})
		} else {
			if jsonData, err := json.Marshal(result); err != nil {
				content = append(content, Content{Type: "text", Text: fmt.Sprintf("%v", result)})
			} else {
				content = append(content, Content{Type: "text", Text: string(jsonData)})
			}
		}

		return &ToolResult{
			Content:           content,
			StructuredContent: result,
			IsError:           false,
		}, nil
	}, normalizationConfig)
}

// RegisterBridgeTools registers the legacy bridge management tools (for compatibility)
func (s *Server) RegisterBridgeTools(ctrl interface{}, client interface{}) {
	// Legacy bridge tools - kept for compatibility but not used in new architecture
	// In the new architecture, game management is done through games.* tools
}

// getGameFromController extracts game config from controller - helper for status checking
func (s *Server) getGameFromController(controller process.ControllerInterface) *config.GameConfig {
	// This is a temporary helper. In a proper refactor, we'd store the game config
	// alongside the controller, but for minimal changes, we'll work with what we have.
	// We can check the controller's spec to get the StopProcessName
	if controller == nil {
		return nil
	}

	// Create a minimal game config with the info we need
	return &config.GameConfig{
		StopProcessName: controller.GetStopProcessName(),
	}
}

// resolveGameId tries to find a game by ID or by target (for better UX)
// Returns the actual game config and whether it was found
// resolveGameReference resolves a game reference with full Stage 1
// semantics: exact ID, unique target match, ambiguity, or absence.
// code is "" on success, else game_not_found | ambiguous_game_reference
// with sorted candidates (design/05-start-pipeline.md).
func resolveGameReference(cfg *config.GamesConfig, ref string) (*config.GameConfig, string, []string) {
	if game, exists := cfg.GetGame(ref); exists {
		return game, "", nil
	}
	var matches []config.GameConfig
	for _, game := range cfg.ListGames() {
		if game.Target == ref {
			matches = append(matches, game)
		}
	}
	switch len(matches) {
	case 1:
		g := matches[0]
		return &g, "", nil
	case 0:
		ids := make([]string, 0, len(cfg.Games))
		for id := range cfg.Games {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return nil, "game_not_found", ids
	default:
		ids := make([]string, 0, len(matches))
		for _, g := range matches {
			ids = append(ids, g.ID)
		}
		sort.Strings(ids)
		return nil, "ambiguous_game_reference", ids
	}
}

// resolveGameResult wraps resolveGameReference into a structured tool error.
func resolveGameResult(cfg *config.GamesConfig, ref string) (*config.GameConfig, *ToolResult) {
	game, code, candidates := resolveGameReference(cfg, ref)
	if game != nil {
		return game, nil
	}
	msg := fmt.Sprintf("Game '%s' not found. Use games_list to see available games.", ref)
	if code == "ambiguous_game_reference" {
		msg = fmt.Sprintf("Game reference '%s' matches multiple configured games (%s). Use the exact game ID.", ref, strings.Join(candidates, ", "))
	}
	structured := map[string]interface{}{"code": code}
	if len(candidates) > 0 {
		structured["candidates"] = candidates
	}
	return nil, &ToolResult{
		Content:           []Content{{Type: "text", Text: msg}},
		IsError:           true,
		StructuredContent: structured,
	}
}

func (s *Server) resolveGameId(gamesConfig *config.GamesConfig, gameIdOrTarget string) (*config.GameConfig, bool) {
	// Deterministic wrapper: an ambiguous target reference resolves to
	// nothing rather than to a map-iteration-order pick.
	game, _, _ := resolveGameReference(gamesConfig, gameIdOrTarget)
	return game, game != nil
}

func mcpNextAction(tool string, arguments map[string]interface{}, reason string) map[string]interface{} {
	action := map[string]interface{}{
		"tool":      tool,
		"arguments": arguments,
	}
	if reason != "" {
		action["reason"] = reason
	}
	return action
}

func gameConfigStructured(game config.GameConfig) map[string]interface{} {
	item := map[string]interface{}{
		"gameId":             game.ID,
		"name":               game.Name,
		"launchMode":         game.LaunchMode,
		"target":             game.Target,
		"hasStopProcessName": game.StopProcessName != "",
	}
	if game.Description != "" {
		item["description"] = game.Description
	}
	if game.WorkingDir != "" {
		item["workingDir"] = game.WorkingDir
	}
	if len(game.Args) > 0 {
		item["args"] = game.Args
	}
	if game.StopProcessName != "" {
		item["stopProcessName"] = game.StopProcessName
	}
	if game.GABPMode != "" {
		item["gabpMode"] = game.GABPMode
	}
	return item
}

func gameValidationWarnings(game config.GameConfig) []string {
	warnings := make([]string, 0, 2)
	if (game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId") && game.StopProcessName == "" {
		warnings = append(warnings, fmt.Sprintf("%s games need stopProcessName for reliable games_stop and games_kill.", game.LaunchMode))
	}
	if launcherModeIgnoresConfiguredArgs(game) {
		if game.LaunchMode == "SteamAppId" {
			warnings = append(warnings, fmt.Sprintf("%s launcher URL mode does not pass configured args to the game; run 'gabs games repair %s' to switch to managed Steam launch, or use DirectPath/CustomCommand for custom launchers and arguments such as -savedatafolder=...", game.LaunchMode, game.ID))
		} else {
			warnings = append(warnings, fmt.Sprintf("%s launch mode does not pass configured args to the game; use DirectPath, CustomCommand, or the game launcher's own launch options for arguments such as -savedatafolder=...", game.LaunchMode))
		}
	}
	return warnings
}

func launcherModeIgnoresConfiguredArgs(game config.GameConfig) bool {
	return (game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId") && len(game.Args) > 0
}

func addValidationWarnings(structured map[string]interface{}, warnings []string) {
	if len(warnings) > 0 {
		structured["validationWarnings"] = warnings
	}
}

func appendValidationWarningText(message string, warnings []string) string {
	if len(warnings) == 0 {
		return message
	}
	return fmt.Sprintf("%s Configuration warning: %s", message, strings.Join(warnings, " "))
}

// addResolvedContext reports the selected profile and applied launch-input
// names (never values) on a start result.
func addResolvedContext(structured map[string]interface{}, r *launch.Resolved) {
	if r == nil {
		return
	}
	if r.Profile != "" {
		structured["activeProfile"] = r.Profile
	}
	if len(r.AppliedInputs) > 0 {
		structured["appliedLaunchInputs"] = r.AppliedInputs
	}
	structured["configRevision"] = r.ConfigRevision
}

// configFilePathHint returns the actual config path for error guidance.
func (s *Server) configFilePathHint() string {
	if s.configStore != nil {
		return s.configStore.Path()
	}
	if cp, err := config.NewConfigPaths(s.configDir); err == nil {
		return cp.GetMainConfigPath()
	}
	return "config.json"
}

// sortedProfileNames returns the profile names in deterministic order.
func sortedProfileNames(profiles map[string]config.ProfileConfig) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// profilesStructured renders profile discovery metadata: names and
// descriptions only — arg/env templates are noise, not secret, and stay out
// of results (design/10-mcp-surface.md).
func profilesStructured(profiles map[string]config.ProfileConfig) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(profiles))
	for _, name := range sortedProfileNames(profiles) {
		item := map[string]interface{}{"name": name}
		if d := profiles[name].Description; d != "" {
			item["description"] = d
		}
		out = append(out, item)
	}
	return out
}

// launchInputsStructured renders the JSON-Schema-style input map with every
// declared constraint, including the effective default maxLength, so an
// agent can form a valid value without trial calls.
func launchInputsStructured(inputs map[string]config.LaunchInputConfig) map[string]interface{} {
	out := make(map[string]interface{}, len(inputs))
	for name, in := range inputs {
		item := map[string]interface{}{
			"type":        in.Type,
			"description": in.Description,
		}
		if len(in.Enum) > 0 {
			item["enum"] = append([]string(nil), in.Enum...)
		}
		if in.Minimum != nil {
			item["minimum"] = *in.Minimum
		}
		if in.Maximum != nil {
			item["maximum"] = *in.Maximum
		}
		if in.Type == "string" {
			maxLen := config.InputMaxLengthDefault
			if in.MaxLength != nil {
				maxLen = *in.MaxLength
			}
			item["maxLength"] = maxLen
			if in.Pattern != "" {
				// RE2 syntax, matched against the entire value.
				item["pattern"] = in.Pattern
				item["patternDialect"] = "re2-full-match"
			}
		}
		if len(in.Profiles) > 0 {
			item["profiles"] = append([]string(nil), in.Profiles...)
		}
		out[name] = item
	}
	return out
}

// escapeJSONPointerToken escapes one RFC 6901 token — warning paths are
// emitted escaped, so unescaped prefixes would never match an ID containing
// ~ or /.
func escapeJSONPointerToken(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

// gameConfigWarnings filters load warnings owned by one game.
func gameConfigWarnings(cfg *config.GamesConfig, gameID string) []string {
	prefix := "/games/" + escapeJSONPointerToken(gameID)
	var out []string
	for _, w := range cfg.Warnings {
		if strings.HasPrefix(w.Path, prefix+"/") || w.Path == prefix {
			out = append(out, w.String())
		}
	}
	return out
}

// modeIncompatibleIssues returns the requested game's validation issues iff
// they are all mode-incompatibility findings — the Stage 1 code
// launch_mode_incompatible must be emitted for "mode rejects profiles/
// inputs/env" instead of the generic config_invalid (design/05).
func modeIncompatibleIssues(cfgErr *config.ConfigError, gameID string) []string {
	var ve *config.ValidationError
	if cfgErr == nil || !errors.As(cfgErr.Err, &ve) {
		return nil
	}
	prefix := "/games/" + escapeJSONPointerToken(gameID)
	var msgs []string
	for _, is := range ve.Issues {
		if is.Path != prefix && !strings.HasPrefix(is.Path, prefix+"/") {
			continue
		}
		if is.Code != config.IssueCodeModeIncompatible {
			return nil // mixed failure: the generic stale-config refusal stands
		}
		msgs = append(msgs, is.String())
	}
	return msgs
}

// attachConfigHealth adds global config warnings (those without an owning
// game — MCP callers without a CLI must still see them) and any active
// config error to a structured result.
func attachConfigHealth(structured map[string]interface{}, cfg *config.GamesConfig, cfgErr *config.ConfigError) {
	var global []string
	for _, w := range cfg.Warnings {
		if !strings.HasPrefix(w.Path, "/games/") {
			global = append(global, w.String())
		}
	}
	if len(global) > 0 {
		if existing, ok := structured["configWarnings"].([]string); ok {
			structured["configWarnings"] = append(existing, global...)
		} else {
			structured["configWarnings"] = global
		}
	}
	if cfgErr != nil {
		structured["configError"] = cfgErr.Err.Error()
		structured["invalidRevision"] = cfgErr.Revision
	}
}

func (s *Server) gameStatusStructured(game config.GameConfig, status string) map[string]interface{} {
	toolCount := len(s.getGameSpecificTools(game.ID))
	diagnostics := s.gameStateDiagnostics(game, status)
	nextActions := s.nextActionsForGameStatus(game, status, toolCount)
	nextActions = nextActionsForGameStateDiagnostics(game, diagnostics, nextActions)
	item := map[string]interface{}{
		"gameId":            game.ID,
		"name":              game.Name,
		"status":            status,
		"statusDescription": s.getStatusDescriptionFromStatus(status, &game),
		"toolCount":         toolCount,
		"nextActions":       nextActions,
	}
	if diagnostics != nil {
		item["diagnostics"] = diagnostics
	}
	if disconnectNote := s.describeLastGABPDisconnect(game.ID); disconnectNote != "" {
		item["lastDisconnect"] = disconnectNote
	}
	if warnings := gameValidationWarnings(game); len(warnings) > 0 {
		item["validationWarnings"] = warnings
	}
	return item
}

func (s *Server) nextActionsForGameStatus(game config.GameConfig, status string, toolCount int) []map[string]interface{} {
	gameArg := map[string]interface{}{"gameId": game.ID}
	discoverArgs := map[string]interface{}{"gameId": game.ID, "brief": true}

	switch status {
	case process.RuntimeStateStatusStarting:
		return []map[string]interface{}{
			mcpNextAction("games_status", gameArg, "Wait for startup to finish before issuing more game commands."),
		}
	case "stopped":
		return []map[string]interface{}{
			mcpNextAction("games_start", gameArg, "Start the game before connecting to GABP tools."),
		}
	case "stale-runtime-cleaned":
		return []map[string]interface{}{
			mcpNextAction("games_start", gameArg, "Start the game with fresh runtime state."),
		}
	case "shared-running":
		return []map[string]interface{}{
			mcpNextAction("games_connect", gameArg, "Attach this GABS session to the already running game bridge."),
		}
	case "running", "connected":
		if toolCount > 0 {
			return []map[string]interface{}{
				mcpNextAction("games_tool_names", discoverArgs, "Discover connected game-specific tools."),
			}
		}
		return []map[string]interface{}{
			mcpNextAction("games_connect", gameArg, "Connect or re-sync GABP tools for the running game."),
		}
	case "running-disconnected", "disconnected":
		return []map[string]interface{}{
			mcpNextAction("games_connect", gameArg, "Reconnect after the GABP bridge disconnected or finished loading."),
		}
	case "launcher-running", "launcher-triggered":
		return []map[string]interface{}{
			mcpNextAction("games_status", gameArg, "Poll until the real game process or GABP bridge becomes visible."),
			mcpNextAction("games_show", gameArg, "Check whether stopProcessName is configured for launcher-based lifecycle control."),
		}
	default:
		return []map[string]interface{}{
			mcpNextAction("games_status", gameArg, "Refresh the game status before deciding the next action."),
		}
	}
}

type toolSchemaProperty struct {
	Name         string
	Type         string
	Description  string
	Required     bool
	Nullable     bool
	HasDefault   bool
	DefaultValue interface{}
}

func toolCanonicalName(tool Tool) string {
	if tool.Meta != nil {
		if originalName, ok := tool.Meta["originalName"].(string); ok && originalName != "" {
			return originalName
		}
		if legacyName, ok := tool.Meta[toolMetaLegacyName].(string); ok && legacyName != "" {
			return legacyName
		}
		if gabpName, ok := tool.Meta[toolMetaGABPName].(string); ok && gabpName != "" {
			return gabpName
		}
	}
	return tool.Name
}

func toolLocalName(gameID string, tool Tool) string {
	if gabpName := toolMetaString(tool, toolMetaGABPName); gabpName != "" {
		return gabpName
	}

	dotPrefix := gameID + "."
	slashPrefix := gameID + "/"

	if canonical := toolCanonicalName(tool); strings.HasPrefix(canonical, dotPrefix) {
		return strings.TrimPrefix(canonical, dotPrefix)
	} else if strings.HasPrefix(canonical, slashPrefix) {
		return strings.TrimPrefix(canonical, slashPrefix)
	}
	if strings.HasPrefix(tool.Name, dotPrefix) {
		return strings.TrimPrefix(tool.Name, dotPrefix)
	} else if strings.HasPrefix(tool.Name, slashPrefix) {
		return strings.TrimPrefix(tool.Name, slashPrefix)
	}

	return toolCanonicalName(tool)
}

func toolBelongsToGame(tool Tool, gameID string) bool {
	dotPrefix := gameID + "."
	slashPrefix := gameID + "/"
	return strings.HasPrefix(tool.Name, dotPrefix) ||
		strings.HasPrefix(tool.Name, slashPrefix) ||
		strings.HasPrefix(toolCanonicalName(tool), dotPrefix) ||
		strings.HasPrefix(toolCanonicalName(tool), slashPrefix) ||
		strings.HasPrefix(toolMetaString(tool, toolMetaQualifiedGABPName), slashPrefix) ||
		strings.HasPrefix(toolMetaString(tool, toolMetaLegacyName), dotPrefix)
}

func toolMatchesRequestedName(gameID string, tool Tool, requested string) bool {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return false
	}

	for _, alias := range toolNameAliases(gameID, tool) {
		if requested == alias {
			return true
		}
	}

	return false
}

func gabpToolNameFromTool(gameID string, tool Tool) string {
	if gabpName := toolMetaString(tool, toolMetaGABPName); gabpName != "" {
		return gabpName
	}

	mirroredName := toolCanonicalName(tool)
	gamePrefix := gameID + "."

	if strings.HasPrefix(mirroredName, gamePrefix) {
		mirroredName = strings.TrimPrefix(mirroredName, gamePrefix)
	} else if strings.HasPrefix(tool.Name, gamePrefix) {
		mirroredName = strings.TrimPrefix(tool.Name, gamePrefix)
	}

	return strings.ReplaceAll(mirroredName, ".", "/")
}

func (s *Server) registerGameToolAliasesLocked(gameID, gabpName, exposedName string) {
	if s.gameToolAliases == nil {
		s.gameToolAliases = make(map[string]gameToolAlias)
	}

	alias := gameToolAlias{
		GameID:  gameID,
		GABP:    gabpName,
		Exposed: exposedName,
	}
	for _, name := range []string{
		exposedName,
		gabpName,
		localLegacyMCPToolName(gabpName),
		legacyMCPToolName(gameID, gabpName),
		qualifiedGABPToolName(gameID, gabpName),
	} {
		if strings.TrimSpace(name) != "" {
			s.gameToolAliases[name] = alias
		}
	}
}

func (s *Server) deleteGameToolAliasesLocked(gameID string) {
	for name, alias := range s.gameToolAliases {
		if alias.GameID == gameID {
			delete(s.gameToolAliases, name)
		}
	}
}

func (s *Server) resolveKnownGABPToolAlias(gameID, requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if alias, ok := s.gameToolAliases[requested]; ok && alias.GameID == gameID {
		return alias.GABP, true
	}

	for _, toolName := range s.gameTools[gameID] {
		handler, exists := s.tools[toolName]
		if !exists {
			continue
		}
		if toolMatchesRequestedName(gameID, handler.Tool, requested) {
			if gabpName := gabpToolNameFromTool(gameID, handler.Tool); gabpName != "" {
				return gabpName, true
			}
		}
	}

	return "", false
}

func (s *Server) safeMCPToolNameForGABPTool(gameID, gabpName string) string {
	candidate := safeMCPToolName(gameID, gabpName, 64)

	s.mu.RLock()
	handler, toolExists := s.tools[candidate]
	alias, aliasExists := s.gameToolAliases[candidate]
	s.mu.RUnlock()

	if !toolExists && !aliasExists {
		return candidate
	}
	if toolExists && gabpToolNameFromTool(gameID, handler.Tool) == gabpName {
		return candidate
	}
	if aliasExists && alias.GameID == gameID && alias.GABP == gabpName {
		return candidate
	}

	return safeMCPToolNameWithCollisionSuffix(gameID, gabpName, 64)
}

func (s *Server) cacheGABPToolAliases(gameID string, tools []gabp.ToolDescriptor) {
	for _, tool := range tools {
		gabpName := canonicalGABPToolName(tool.Name)
		if gabpName == "" {
			continue
		}
		exposedName := s.safeMCPToolNameForGABPTool(gameID, gabpName)
		s.mu.Lock()
		s.registerGameToolAliasesLocked(gameID, gabpName, exposedName)
		s.mu.Unlock()
	}
}

func (s *Server) refreshGABPToolAliases(client *gabp.Client, gameID string, timeout time.Duration) error {
	tools, err := client.ListToolsWithTimeout(timeout)
	if err != nil {
		return err
	}
	s.cacheGABPToolAliases(gameID, tools)
	return nil
}

func (s *Server) callDirectGABPTool(gamesConfig *config.GamesConfig, gameIDArg string, hasGameID bool, requested string, args map[string]interface{}, timeout time.Duration) (*ToolResult, bool) {
	gameID, result, handled := s.resolveDirectGABPToolGame(gamesConfig, gameIDArg, hasGameID, requested)
	if handled {
		return result, true
	}
	if gameID == "" {
		return nil, false
	}

	client, _ := s.claimBoundClient(gameID)
	if client == nil {
		return nil, false
	}

	if blocked := s.ensureRuntimeOwnershipForGameCall(gameID, fmt.Sprintf("direct tool '%s'", requested), timeout); blocked != nil {
		return blocked, true
	}

	candidates := make([]string, 0, 2)
	addCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range candidates {
			if existing == value {
				return
			}
		}
		candidates = append(candidates, value)
	}

	if resolved, ok := s.resolveKnownGABPToolAlias(gameID, requested); ok {
		addCandidate(resolved)
	} else if !strings.ContainsAny(strings.TrimSpace(requested), "./") {
		if err := s.refreshGABPToolAliases(client, gameID, timeout); err != nil {
			s.log.Debugw("failed to refresh GABP tool aliases for direct call", "gameId", gameID, "tool", requested, "error", err)
		} else if resolved, ok := s.resolveKnownGABPToolAlias(gameID, requested); ok {
			addCandidate(resolved)
		}
	}

	for _, candidate := range gabpToolNameFromDelimitedRequest(gameID, requested) {
		addCandidate(candidate)
	}
	if len(candidates) == 0 {
		return nil, false
	}

	attentionToolNames := append([]string{requested}, candidates...)
	if !s.shouldBypassAttentionGateForRequest(gameID, attentionToolNames...) {
		if blocked := s.enforceAttentionGate(gameID, requested, client); blocked != nil {
			return blocked, true
		}
	}

	var firstErr error
	var lastErr error
	for _, candidate := range candidates {
		callResult, isError, err := client.CallToolWithTimeout(candidate, args, timeout)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			lastErr = err
			if isGABPToolNotFoundError(err) {
				continue
			}
			return s.gabpCallErrorResult(gameID, err), true
		}

		if isError {
			return &ToolResult{
				Content:           []Content{{Type: "text", Text: fmt.Sprintf("Tool error: %v", callResult)}},
				StructuredContent: callResult,
				IsError:           true,
			}, true
		}

		return gabpCallSuccessResult(callResult), true
	}

	if firstErr == nil {
		return nil, false
	}
	if lastErr != nil && !isGABPToolNotFoundError(lastErr) {
		return s.gabpCallErrorResult(gameID, lastErr), true
	}
	return s.gabpCallErrorResult(gameID, firstErr), true
}

func (s *Server) resolveDirectGABPToolGame(gamesConfig *config.GamesConfig, gameIDArg string, hasGameID bool, requested string) (string, *ToolResult, bool) {
	if hasGameID {
		game, exists := s.resolveGameId(gamesConfig, gameIDArg)
		if !exists {
			return "", &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games_list to see available games.", gameIDArg)}},
				IsError: true,
			}, true
		}
		return game.ID, nil, false
	}

	s.mu.RLock()
	if alias, ok := s.gameToolAliases[strings.TrimSpace(requested)]; ok {
		s.mu.RUnlock()
		return alias.GameID, nil, false
	}
	s.mu.RUnlock()

	connectedGameIDs := make([]string, 0)
	for _, game := range gamesConfig.ListGames() {
		if client, _ := s.claimBoundClient(game.ID); client != nil {
			connectedGameIDs = append(connectedGameIDs, game.ID)
		}
	}

	matches := make([]string, 0, 1)
	for _, gameID := range connectedGameIDs {
		if strings.HasPrefix(requested, gameID+".") || strings.HasPrefix(requested, gameID+"/") || strings.HasPrefix(requested, gameID+"_") {
			matches = append(matches, gameID)
		}
	}

	if len(matches) == 1 {
		return matches[0], nil, false
	}
	if len(matches) > 1 {
		sort.Strings(matches)
		return "", &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Tool '%s' matched multiple connected games (%s). Include gameId.", requested, strings.Join(matches, ", "))}},
			IsError: true,
		}, true
	}

	if len(connectedGameIDs) == 1 {
		return connectedGameIDs[0], nil, false
	}

	return "", nil, false
}

func (s *Server) gabpCallErrorResult(gameID string, err error) *ToolResult {
	disconnectNote := s.describeLastGABPDisconnect(gameID)
	if disconnectNote != "" {
		disconnectNote = " " + disconnectNote
	}
	return &ToolResult{
		Content: []Content{{Type: "text", Text: fmt.Sprintf("GABP tool call failed: %v.%s", err, disconnectNote)}},
		IsError: true,
	}
}

func gabpCallSuccessResult(result map[string]interface{}) *ToolResult {
	content := []Content{}
	if resultText, ok := result["text"].(string); ok {
		content = append(content, Content{Type: "text", Text: resultText})
	} else {
		if jsonData, err := json.Marshal(result); err != nil {
			content = append(content, Content{Type: "text", Text: fmt.Sprintf("%v", result)})
		} else {
			content = append(content, Content{Type: "text", Text: string(jsonData)})
		}
	}

	return &ToolResult{
		Content:           content,
		StructuredContent: result,
		IsError:           false,
	}
}

func isGABPToolNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "tool") && strings.Contains(message, "not found")
}

func toolBriefDescription(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}

	if newline := strings.IndexByte(description, '\n'); newline >= 0 {
		description = strings.TrimSpace(description[:newline])
	}

	if sentenceEnd := strings.Index(description, ". "); sentenceEnd >= 0 {
		description = strings.TrimSpace(description[:sentenceEnd+1])
	}

	const maxLen = 140
	if len(description) <= maxLen {
		return description
	}

	return strings.TrimSpace(description[:maxLen-3]) + "..."
}

func getRequiredSchemaFields(schema map[string]interface{}) map[string]struct{} {
	requiredFields := make(map[string]struct{})
	if schema == nil {
		return requiredFields
	}

	switch required := schema["required"].(type) {
	case []string:
		for _, field := range required {
			requiredFields[field] = struct{}{}
		}
	case []interface{}:
		for _, field := range required {
			if name, ok := field.(string); ok {
				requiredFields[name] = struct{}{}
			}
		}
	}

	return requiredFields
}

func getSchemaTypeString(definition map[string]interface{}) (string, bool) {
	nullable := false

	switch rawType := definition["type"].(type) {
	case string:
		return rawType, nullable
	case []string:
		types := make([]string, 0, len(rawType))
		for _, item := range rawType {
			if item == "null" {
				nullable = true
				continue
			}
			types = append(types, item)
		}
		if len(types) > 0 {
			return strings.Join(types, " | "), nullable
		}
	case []interface{}:
		types := make([]string, 0, len(rawType))
		for _, item := range rawType {
			typeName, ok := item.(string)
			if !ok {
				continue
			}
			if typeName == "null" {
				nullable = true
				continue
			}
			types = append(types, typeName)
		}
		if len(types) > 0 {
			return strings.Join(types, " | "), nullable
		}
	}

	return "any", nullable
}

func getSchemaProperties(schema map[string]interface{}) []toolSchemaProperty {
	if schema == nil {
		return nil
	}

	rawProperties, ok := schema["properties"].(map[string]interface{})
	if !ok || len(rawProperties) == 0 {
		return nil
	}

	requiredFields := getRequiredSchemaFields(schema)
	names := make([]string, 0, len(rawProperties))
	for name := range rawProperties {
		names = append(names, name)
	}
	sort.Strings(names)

	properties := make([]toolSchemaProperty, 0, len(names))
	for _, name := range names {
		property := toolSchemaProperty{
			Name:     name,
			Type:     "any",
			Required: false,
		}

		if _, ok := requiredFields[name]; ok {
			property.Required = true
		}

		if rawDefinition, ok := rawProperties[name].(map[string]interface{}); ok {
			property.Type, property.Nullable = getSchemaTypeString(rawDefinition)
			if description, ok := rawDefinition["description"].(string); ok {
				property.Description = description
			}
			if nullable, ok := rawDefinition["nullable"].(bool); ok && nullable {
				property.Nullable = true
			}
			if defaultValue, ok := rawDefinition["default"]; ok {
				property.HasDefault = true
				property.DefaultValue = defaultValue
			}
		}

		properties = append(properties, property)
	}

	return properties
}

func formatSchemaDefaultValue(value interface{}) string {
	if encoded, err := json.Marshal(value); err == nil {
		return string(encoded)
	}
	return fmt.Sprintf("%v", value)
}

// writeToolParams writes parameter and output schema info for a tool to the content builder
func writeToolParams(content *strings.Builder, tool Tool) {
	inputProperties := getSchemaProperties(tool.InputSchema)
	if len(inputProperties) > 0 {
		content.WriteString("\n  Parameters:")
		for _, property := range inputProperties {
			tags := []string{property.Type}
			if !property.Required {
				tags = append(tags, "optional")
			}
			if property.HasDefault {
				tags = append(tags, "default: "+formatSchemaDefaultValue(property.DefaultValue))
			}

			if property.Description != "" {
				content.WriteString(fmt.Sprintf("\n    - `%s` (%s): %s", property.Name, strings.Join(tags, ", "), property.Description))
			} else {
				content.WriteString(fmt.Sprintf("\n    - `%s` (%s)", property.Name, strings.Join(tags, ", ")))
			}
		}
	}

	outputProperties := getSchemaProperties(tool.OutputSchema)
	if len(outputProperties) > 0 {
		content.WriteString("\n  Returns:")
		for _, property := range outputProperties {
			tags := []string{property.Type}
			if property.Nullable {
				tags = append(tags, "optional")
			}

			if property.Description != "" {
				content.WriteString(fmt.Sprintf("\n    - `%s` (%s): %s", property.Name, strings.Join(tags, ", "), property.Description))
			} else {
				content.WriteString(fmt.Sprintf("\n    - `%s` (%s)", property.Name, strings.Join(tags, ", ")))
			}
		}
	}
}

// getGameSpecificTools returns tools that belong to a specific game.
// It prefers explicit game tracking and falls back to prefix matching for
// compatibility with older tests and direct registrations.
func (s *Server) getGameSpecificTools(gameID string) []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]struct{})
	gameTools := make([]Tool, 0)

	addTool := func(tool Tool) {
		if _, exists := seen[tool.Name]; exists {
			return
		}
		if !toolBelongsToGame(tool, gameID) {
			return
		}

		seen[tool.Name] = struct{}{}
		gameTools = append(gameTools, tool)
	}

	if trackedToolNames, exists := s.gameTools[gameID]; exists {
		for _, toolName := range trackedToolNames {
			if handler, exists := s.tools[toolName]; exists {
				addTool(handler.Tool)
			}
		}
	}

	for _, handler := range s.tools {
		addTool(handler.Tool)
	}

	sort.Slice(gameTools, func(i, j int) bool {
		left := toolCanonicalName(gameTools[i])
		right := toolCanonicalName(gameTools[j])
		if left == right {
			return gameTools[i].Name < gameTools[j].Name
		}
		return left < right
	})

	return gameTools
}

func (s *Server) ensureGameToolsMirrored(gameID string, timeout time.Duration) error {
	if len(s.getGameSpecificTools(gameID)) > 0 {
		return nil
	}

	client, _ := s.claimBoundClient(gameID)
	if client == nil {
		return nil
	}

	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	if err := s.syncGABPToolsWithTimeout(client, gameID, timeout); err != nil {
		return err
	}
	if err := s.exposeGABPResources(client, gameID); err != nil {
		return err
	}
	return nil
}

// checkGameStatus returns the current status of a game
// getStatusDescription provides a user-friendly description of the game status
func (s *Server) getStatusDescription(gameID string, gameConfig *config.GameConfig) string {
	status := s.checkGameStatus(gameID)
	return s.getStatusDescriptionFromStatus(status, gameConfig)
}

// getStatusDescriptionFromStatus provides a user-friendly description from a status string
// This avoids calling checkGameStatus again when the status is already known
func (s *Server) getStatusDescriptionFromStatus(status string, gameConfig *config.GameConfig) string {
	switch status {
	case process.RuntimeStateStatusStarting:
		return "starting (another GABS session is launching the game)"
	case "shared-running":
		return "running (another GABS session owns the process; use games_connect to attach)"
	case "running-disconnected":
		return "running, but the GABP bridge disconnected"
	case "running":
		// Check if this is a launcher-based game with process tracking
		if gameConfig.LaunchMode == "SteamAppId" || gameConfig.LaunchMode == "EpicAppId" {
			if gameConfig.StopProcessName != "" {
				return "running (GABS is tracking the game process)"
			}
		}
		return "running (GABS controls the process)"
	case "connected":
		return "running (connected via GABP; process not managed by this GABS instance)"
	case "disconnected":
		return "GABP disconnected (the game may have crashed or closed the bridge)"
	case "stale-runtime-cleaned":
		return "stopped (stale runtime state was removed)"
	case process.PhaseStopping:
		return "stopping (a bounded stop operation is in progress)"
	case process.PhaseKilling:
		return "killing (a bounded force-termination operation is in progress)"
	case "stopped":
		return "stopped"
	case "launcher-running":
		return fmt.Sprintf("launcher active (game may be starting via %s)", gameConfig.LaunchMode)
	case "launcher-triggered":
		return fmt.Sprintf("launched via %s (GABS cannot track the game process - no stopProcessName configured)", gameConfig.LaunchMode)
	default:
		return status
	}
}

func (s *Server) describeLastGABPDisconnect(gameID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.describeLastGABPDisconnectLocked(gameID)
}

func (s *Server) describeLastGABPDisconnectLocked(gameID string) string {
	record, exists := s.gabpDisconnects[gameID]
	if !exists {
		return ""
	}

	return fmt.Sprintf("Last GABP disconnect at %s: %s", record.At.Format(time.RFC3339), record.Message)
}

func (s *Server) recordGABPDisconnectLocked(gameID string, err error) {
	message := "connection closed"
	if err != nil {
		message = err.Error()
	}

	s.gabpDisconnects[gameID] = gabpDisconnectRecord{
		At:      time.Now().UTC(),
		Message: message,
	}
}

func (s *Server) clearGABPDisconnectLocked(gameID string) {
	delete(s.gabpDisconnects, gameID)
}

// dispatchGABPDisconnect runs the peer-close handler under Shutdown's join
// (round 12 F4). The shutdownCh check and disconnectWG.Add happen together
// under s.mu, atomic with Shutdown closing shutdownCh under the same lock, so
// no handler can start a clearBridgeAttachment write after Shutdown begins
// waiting — the fix for the disconnect writer racing TempDir teardown.
func (s *Server) dispatchGABPDisconnect(gameID string, client *gabp.Client, err error) {
	s.mu.Lock()
	select {
	case <-s.shutdownCh:
		s.mu.Unlock()
		return
	default:
	}
	s.disconnectWG.Add(1)
	s.mu.Unlock()
	defer s.disconnectWG.Done()
	s.HandleUnexpectedGABPDisconnect(gameID, client, err)
}

// HandleUnexpectedGABPDisconnect records bridge loss and removes mirrored tools immediately.
func (s *Server) HandleUnexpectedGABPDisconnect(gameID string, client *gabp.Client, err error) {
	s.mu.Lock()
	current, exists := s.gabpClients[gameID]
	if !exists || current != client {
		s.mu.Unlock()
		return
	}

	s.recordGABPDisconnectLocked(gameID, err)
	resourcesChanged := len(s.gameResources[gameID]) > 0
	s.clearGameAttentionStateLocked(gameID)
	s.cleanupGameResourcesInternal(gameID)
	attachmentRef, hadAttachment := s.takeBridgeAttachmentRefLocked(gameID)
	s.mu.Unlock()

	if hadAttachment {
		// A matching disconnect clears the persisted attachment record;
		// the connection identity fences out stale callbacks (design/06).
		s.clearBridgeAttachment(gameID, attachmentRef.launchID, attachmentRef.connectionID)
	}
	if resourcesChanged {
		s.SendResourcesListChangedNotification()
	}

	s.log.Warnw("unexpected GABP disconnect", "gameId", gameID, "error", err)
}

// resolveClaimStatusByLiveness judges a current-schema claim by the one
// liveness rule over its pinned context (design/04) — never the legacy
// PID/name-only resolver, which would delete a hook-only external snapshot
// as stale without ever running its pinned status hook. unknown never
// cleans state; absence never clears a completed-unobserved claim.
func (s *Server) resolveClaimStatusByLiveness(gameID string, claim *process.RuntimeState, gabpLive bool) string {
	status, _ := s.resolveClaimStatusObserved(gameID, claim, gabpLive)
	return status
}

// resolveClaimStatusObserved is resolveClaimStatusByLiveness plus the
// observation itself: verdict, source, detail, hook facts, and warnings —
// unknown says what was observed, and contradictions carry their warning
// (design/04; review round 8). When a fenced write loses to a successor
// mid-evaluation, it reloads the CURRENT claim and re-runs the full
// claim-first evaluation (never maps phase to status) — phase is not
// liveness evidence and an active successor can itself be stopped or
// unknown (review round 9).
func (s *Server) resolveClaimStatusObserved(gameID string, claim *process.RuntimeState, gabpLive bool) (string, *process.LivenessEvidence) {
	for attempt := 0; attempt < 4; attempt++ {
		status, ev, superseded := s.evaluateClaimStatusOnce(gameID, claim, gabpLive)
		if !superseded {
			return status, ev
		}
		cur, lerr := process.LoadRuntimeState(gameID, s.configDir)
		if lerr != nil {
			return "unknown", nil
		}
		if cur == nil {
			return "stale-runtime-cleaned", ev
		}
		if cur.SchemaVersion < process.RuntimeSchemaVersion {
			// A legacy successor: the caller's legacy path owns it.
			return "", nil
		}
		// Re-derive the bridge binding for the freshly loaded claim.
		claim = cur
		gabpLive = s.boundGABPForClaim(gameID, cur)
	}
	// Convergence guard: repeated supersession means the claim is churning;
	// report unknown rather than loop.
	return "unknown", nil
}

// evaluateClaimStatusOnce runs one claim-first evaluation. superseded=true
// means a fenced write lost to a successor and the caller must reload and
// re-evaluate rather than trust this pass.
func (s *Server) evaluateClaimStatusOnce(gameID string, claim *process.RuntimeState, gabpLive bool) (string, *process.LivenessEvidence, bool) {
	var hook *launch.ResolvedHook
	if claim.Lifecycle != nil {
		hook = claim.Lifecycle.Status
	}
	profile := claim.Profile
	if claim.Source == process.SourceExternal && claim.ObservedProfile != "" && claim.ObservedProfile != process.ObservedProfileUnknown {
		profile = claim.ObservedProfile
	}
	now := time.Now().UTC()

	// Restart recovery is lazy and liveness-driven (design/07): a dead
	// bounded attempt — executor provably gone or deadline expired — is
	// normalized on this first observation. Stop/kill attempts are recorded
	// as lastActionResult interrupted with the phase following liveness;
	// the crash-during-spawn window promotes, removes, or stays occupied
	// per its evidence. A dead attempt never renders as in progress, and
	// the interrupted hook is never replayed.
	if claim.Operation != nil && !process.OperationInFlight(claim.Operation, now) {
		rec, rerr := process.RecoverInterruptedClaim(gameID, s.configDir, s.instanceID, claim, gabpLive, s.selfConnectionFor(gameID, claim.LaunchID), now)
		if rerr != nil {
			s.log.Warnw("claim recovery failed", "gameId", gameID, "error", rerr)
			return "unknown", nil, false
		}
		if rec != nil {
			if rec.Superseded {
				// The evaluated generation lost its fence mid-recovery:
				// reload and re-evaluate the CURRENT claim (round 9).
				return "", nil, true
			}
			if rec.Removed {
				return "stale-runtime-cleaned", &rec.Evidence, false
			}
			if rec.Claim != nil {
				claim = rec.Claim
			}
			if claim.Phase == process.PhaseActive {
				if rec.Evidence.Verdict == process.StatusRunning {
					return process.RuntimeStateStatusRunning, &rec.Evidence, false
				}
				return "unknown", &rec.Evidence, false
			}
			// Preflight with a live-but-overdue executor, or the spawn
			// window with genuinely unknown evidence: occupied.
			return process.RuntimeStateStatusStarting, &rec.Evidence, false
		}
	}

	// An in-flight bounded operation owns its claim: status truthfully
	// reports the persisted phase with the attempt's timing (design/06) and
	// never cleans the claim out from under the executor — completion or
	// interruption recovery does that.
	opInFlight := process.OperationInFlight(claim.Operation, now)

	ev := process.EvaluateLiveness(process.LivenessInput{
		GABPLive:         gabpLive,
		CallerInstanceID: s.instanceID,
		Claim:            claim,
		StatusHook:       hook,
		GameID:           gameID,
		Profile:          profile,
	})
	switch ev.Verdict {
	case process.StatusRunning:
		switch claim.Phase {
		case process.PhaseStarting:
			if claim.Operation == nil {
				// Passive promotion (design/20): running seen by a status
				// observation promotes a completed-unobserved claim to
				// active; an in-flight start owns its own completion.
				if _, err := process.FencedTransitionThen(gameID, s.configDir, claim.LaunchID, "", func(st *process.RuntimeState) error {
					if st.Operation != nil || st.Phase != process.PhaseStarting {
						return process.ErrFencingViolation
					}
					st.Phase = process.PhaseActive
					st.Status = process.RuntimeStateStatusRunning
					return nil
				}, func(st *process.RuntimeState) {
					// Stage 4 verification by status observation: credit the
					// start from the pinned identity AFTER the flip commits
					// (round 11 P1-2; round 13 F5 — afterCommit so history never
					// advances ahead of the runtime save).
					s.applyPinnedWorkloadStart(gameID, st)
				}); err == nil {
					return process.RuntimeStateStatusRunning, &ev, false
				}
			}
			return process.RuntimeStateStatusStarting, &ev, false
		case process.PhaseStopping, process.PhaseKilling:
			if opInFlight {
				return claim.Phase, &ev, false
			}
		}
		return process.RuntimeStateStatusRunning, &ev, false
	case process.StatusUnknown:
		switch claim.Phase {
		case process.PhaseStarting:
			return process.RuntimeStateStatusStarting, &ev, false
		case process.PhaseStopping, process.PhaseKilling:
			if opInFlight {
				return claim.Phase, &ev, false
			}
		}
		return "unknown", &ev, false
	default: // stopped
		if opInFlight {
			switch claim.Phase {
			case process.PhaseStopping, process.PhaseKilling:
				return claim.Phase, &ev, false
			}
			return process.RuntimeStateStatusStarting, &ev, false
		}
		// Completed-unobserved asymmetry (design/05 Stage 4): absence-based
		// stopped never clears the claim — only positive evidence does.
		if claim.Phase == process.PhaseStarting && claim.Operation == nil && claim.SpawnState == process.SpawnStateSpawned {
			positive := ev.Source == process.LivenessSourceStatusHook ||
				(ev.Source == process.LivenessSourcePID && claim.PIDRole == process.PIDRoleWorkload)
			if !positive {
				return process.RuntimeStateStatusStarting, &ev, false
			}
		}
		// Fenced removal (design/06): the stopped verdict may have taken a
		// seconds-long hook to compute — remove only while the evaluated
		// launch identity still holds and no operation was admitted
		// meanwhile; a stale verdict must never delete a successor claim.
		if err := process.RemoveRuntimeStateIfCurrent(gameID, s.configDir, s.instanceID, claim.LaunchID, s.selfConnectionFor(gameID, claim.LaunchID)); err != nil {
			if errors.Is(err, process.ErrFencingViolation) {
				// A successor exists: reload and re-evaluate it fully —
				// phase is not liveness evidence (round 9).
				return "", &ev, true
			}
			s.log.Warnw("failed to remove stopped runtime claim", "gameId", gameID, "error", err)
			return "", &ev, false
		}
		s.log.Debugw("removed stopped runtime claim", "gameId", gameID, "evidence", ev.Detail)
		return "stale-runtime-cleaned", &ev, false
	}
}

func (s *Server) resolveSharedRuntimeStatus(gameID string, gabpLive bool) string {
	runtimeState, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil {
		s.log.Warnw("failed to read shared runtime state", "gameId", gameID, "error", err)
		return ""
	}
	if runtimeState == nil {
		return ""
	}

	if runtimeState.SchemaVersion >= process.RuntimeSchemaVersion {
		// checkGameStatus snapshots the in-memory state first and calls
		// this with s.mu released: evidence probes (status hooks, process
		// scans) never run under the server mutex.
		return s.resolveClaimStatusByLiveness(gameID, runtimeState, gabpLive)
	}

	status := process.ResolveRuntimeStateStatus(runtimeState)
	if status != "" {
		return status
	}

	if err := process.RemoveRuntimeState(gameID, s.configDir); err != nil {
		s.log.Warnw("failed to remove stale runtime state", "gameId", gameID, "error", err)
	} else {
		s.log.Debugw("removed stale runtime state", "gameId", gameID)
		return "stale-runtime-cleaned"
	}

	return ""
}

// startRefusalError carries a structured Stage 2 refusal (design/05) out of
// startGame; the handler renders its stable code.
type startRefusalError struct {
	refusal  *process.StartRefusal
	warnings []string
}

func (e *startRefusalError) Error() string { return e.refusal.Message }

// addStartAdoption renders the Stage 4 adoption verdict and Stage 2 probe
// warnings into a start result (design/05): adoption means the injected
// context may not have survived a launcher re-exec.
func addStartAdoption(structured map[string]interface{}, message *string, r *process.ProcessStartResult) {
	if r == nil {
		return
	}
	if r.Adopted {
		structured["adopted"] = true
		*message += " Note: the launch was adopted — the direct child exited and the workload was observed by name; injected args/env (including GABS_PROFILE) may not have survived a launcher re-exec. A bridge connection proves the managed environment survived; a verified delivery report proves the full context did."
	}
	if len(r.StartWarnings) > 0 {
		structured["startWarnings"] = r.StartWarnings
	}
}

// startRefusalResult renders a Stage 2 refusal with its stable code and
// per-code next actions.
func (s *Server) startRefusalResult(game config.GameConfig, e *startRefusalError, hc historyContext, validationWarnings []string) *ToolResult {
	ref := e.refusal
	structured := map[string]interface{}{
		"code":   ref.Code,
		"gameId": game.ID,
	}
	// Every failure result carries a causeClass + track record (design/08).
	// Refusals are pre-accept (no history WRITE) — render only. The
	// gating-refusal next actions below stay (all state-class, no config
	// edit); attribution adds the class, track line, and edit notice.
	cls := process.Classify(ref.Code, process.ClassifyContext{})
	structured["causeClass"] = cls.Class
	var entry *process.HistoryEntry
	if h, herr := process.LoadHistory(game.ID, s.configDir); herr == nil {
		if en := h.Profiles[hc.profile]; en != nil && en.ContextHash == hc.contextHash && hc.contextHash != "" {
			entry = en
		}
	}
	structured["trackRecord"] = process.TrackRecordLine(entry)
	if hc.contextHash != "" {
		if notice := s.editNoticeFor(game.ID, hc); notice != "" {
			structured["editNotice"] = notice
		}
	}
	if ref.ActiveProfile != "" || ref.Code == process.RefusalAlreadyRunning {
		structured["activeProfile"] = ref.ActiveProfile
	}
	if ref.RequestedProfile != "" {
		structured["requestedProfile"] = ref.RequestedProfile
	}
	if len(ref.Candidates) > 0 {
		structured["candidates"] = ref.Candidates
	}
	if ref.SnapshotPersisted {
		structured["externalSnapshotPersisted"] = true
	}
	if ref.Evidence != nil {
		structured["evidence"] = map[string]interface{}{
			"verdict": ref.Evidence.Verdict,
			"source":  ref.Evidence.Source,
			"detail":  ref.Evidence.Detail,
		}
	}
	if ref.Phase != "" {
		structured["phase"] = ref.Phase
	}
	if op := ref.Operation; op != nil {
		structured["operation"] = map[string]interface{}{
			"action":           op.Action,
			"attemptStartedAt": op.AttemptStartedAt.Format(time.RFC3339),
			"deadline":         op.Deadline.Format(time.RFC3339),
		}
	}
	if len(e.warnings) > 0 {
		structured["startWarnings"] = e.warnings
	}

	var next []map[string]interface{}
	switch ref.Code {
	case process.RefusalAlreadyRunning:
		next = append(next,
			mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Inspect the active instance."),
			mcpNextAction("games_stop", map[string]interface{}{"gameId": game.ID}, "Stop it first if the other profile is needed."))
	case process.RefusalExternalInstance:
		if ref.SnapshotPersisted {
			next = append(next,
				mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "The external instance is tracked by ID."),
				mcpNextAction("games_stop", map[string]interface{}{"gameId": game.ID}, "Stop the external instance to free the game."))
		} else {
			next = append(next, mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Resolve the candidates manually; GABS never guesses."))
		}
	case process.RefusalOperationInFlight:
		next = append(next, mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Re-check once the in-flight operation finishes."))
	case process.RefusalBlockedUnknown:
		next = append(next, mcpNextAction("games_status", map[string]interface{}{"gameId": game.ID}, "Gather evidence; use repair --forget-runtime only if the instance is provably gone."))
	}
	if len(next) > 0 {
		structured["nextActions"] = next
	}
	addValidationWarnings(structured, validationWarnings)

	// already_running and operation_in_progress are informational: the game
	// is (or is becoming) available, which is what the caller wanted.
	isError := ref.Code != process.RefusalAlreadyRunning && ref.Code != process.RefusalOperationInFlight
	return &ToolResult{
		Content:           []Content{{Type: "text", Text: ref.Message}},
		IsError:           isError,
		StructuredContent: structured,
	}
}

// unobservedStartError is the Stage 4 unobserved outcome: the claim is kept
// in phase starting; not a failure to retry blindly.
type unobservedStartError struct {
	warnings []string
}

func (e *unobservedStartError) Error() string {
	return "nothing observable within the process-start budget"
}

// exitedDuringStartError carries the Stage 4 exit evidence.
type exitedDuringStartError struct {
	exitCode            int
	tail                string
	hookEvidence        string
	warnings            []string
	hookReportedStopped bool
}

func (e *exitedDuringStartError) Error() string {
	return fmt.Sprintf("exited during start (exit code %d)", e.exitCode)
}

// endpointUnavailableError is the structured Stage 2 endpoint-allocation
// failure (design/05): port exhaustion, filesystem failure, occupied cache.
type endpointUnavailableError struct {
	gameID string
	err    error
}

func (e *endpointUnavailableError) Error() string {
	return fmt.Sprintf("failed to prepare GABS endpoint for game '%s': %v", e.gameID, e.err)
}

func (e *endpointUnavailableError) Unwrap() error { return e.err }

func (s *Server) cleanupRuntimeStateInternal(gameId string) {
	if err := process.RemoveRuntimeState(gameId, s.configDir); err != nil {
		s.log.Warnw("failed to cleanup runtime state", "gameId", gameId, "error", err)
	}
}

func (s *Server) adoptProcessBridgeEndpoint(game config.GameConfig, runtimeState *process.RuntimeState, current bridgeEndpoint) (bridgeEndpoint, bool) {
	processEnv := s.inspectGameBridgeEnvironment(game, runtimeState)
	return s.adoptProcessBridgeEndpointFromDiagnostic(game, processEnv, current)
}

func (s *Server) adoptProcessBridgeEndpointFromDiagnostic(game config.GameConfig, processEnv processEnvDiagnostic, current bridgeEndpoint) (bridgeEndpoint, bool) {
	if !processEnv.Present || processEnv.Port <= 0 || strings.TrimSpace(processEnv.Token) == "" {
		return current, false
	}
	if processEnv.GameID != "" && processEnv.GameID != game.ID {
		s.log.Warnw("ignoring bridge environment for different game",
			"gameId", game.ID,
			"processGameId", processEnv.GameID,
			"pid", processEnv.PID)
		return current, false
	}

	if processEnv.Port == current.Port && processEnv.Token == current.Token {
		current.Source = "process-environment"
		current.PID = processEnv.PID
		return current, false
	}

	s.log.Infow("adopted bridge endpoint from running process environment",
		"gameId", game.ID,
		"pid", processEnv.PID,
		"previousPort", current.Port,
		"port", processEnv.Port)

	return bridgeEndpoint{
		Port:   processEnv.Port,
		Token:  processEnv.Token,
		Source: "process-environment",
		PID:    processEnv.PID,
	}, true
}

// errStaleConnectCredential marks a connect refusal where the observed
// process environment carries a previous launch's credentials — the stable
// stale_bridge_credential outcome (design/10).
type errStaleConnectCredential struct {
	gameID string
	pid    int
}

func (e *errStaleConnectCredential) Error() string {
	return fmt.Sprintf("stale bridge credential: the running process (pid %d) carries bridge credentials from a previous launch of '%s'; it cannot serve this launch's bridge — restart the game to pick up the new environment", e.pid, e.gameID)
}

// resolveConnectBridgeEndpoint resolves the ONLY credential games_connect
// may attach with: the current claim's per-launch endpoint (design/03,
// design/07). Process-environment inspection corroborates that exact
// credential or exposes a stale one — it never replaces it. bridge.json is
// never read here: the sole live-attach read of that file is the legacy
// migration candidate captured under the transition lock during
// normalization (design/07), which the connect handler passes explicitly.
func (s *Server) resolveConnectBridgeEndpoint(game config.GameConfig, runtimeState *process.RuntimeState) (bridgeEndpoint, error) {
	if runtimeState == nil {
		return bridgeEndpoint{}, fmt.Errorf("no runtime claim exists for '%s'; nothing is attachable — start the game, or check games_status", game.ID)
	}
	if runtimeState.Source == process.SourceExternal {
		return bridgeEndpoint{}, fmt.Errorf("attachment unavailable: '%s' is an externally started instance and never received this GABS's bridge environment; status/stop/kill remain available", game.ID)
	}
	if runtimeState.Endpoint == nil || runtimeState.Endpoint.Port <= 0 || strings.TrimSpace(runtimeState.Endpoint.Token) == "" {
		return bridgeEndpoint{}, fmt.Errorf("the runtime claim for '%s' carries no attachable endpoint yet; if a start is in flight, re-check games_status", game.ID)
	}

	endpoint := bridgeEndpoint{
		Port:   runtimeState.Endpoint.Port,
		Token:  runtimeState.Endpoint.Token,
		Source: "runtime-claim",
		PID:    runtimeState.GamePID,
	}
	processEnv := s.inspectGameBridgeEnvironment(game, runtimeState)
	switch {
	case processEnv.Present && processEnv.Port == endpoint.Port && processEnv.Token == endpoint.Token:
		endpoint.Source = "process-environment"
		endpoint.PID = processEnv.PID
	case processEnv.Present && processEnv.Port > 0 && strings.TrimSpace(processEnv.Token) != "":
		// The workload demonstrably runs with another launch's
		// credentials: connecting with either credential would be a stale
		// attach — fail with the stable code instead of a timeout.
		return bridgeEndpoint{}, &errStaleConnectCredential{gameID: game.ID, pid: processEnv.PID}
	case readableProcessEnvLacksAttachableBridgeEndpoint(game, processEnv):
		// The workload's environment is readable and provably lacks the
		// bridge variables: it never received a GABS environment, so no
		// dial can succeed — diagnose instead of timing out.
		return bridgeEndpoint{}, fmt.Errorf("%s", processBridgeEnvironmentMissingMessage(game, processEnv))
	}
	return endpoint, nil
}

func (s *Server) attemptStartupGABPConnection(
	controller process.ControllerInterface,
	connector process.GABPConnector,
	gameID string,
	endpoint bridgeEndpoint,
	timeout time.Duration,
) startupConnectResult {
	startedAt := time.Now()
	if timeout <= 0 {
		return startupConnectResult{
			Error:            fmt.Errorf("no synchronous GABP wait requested"),
			GameStillRunning: controllerLooksAliveForMCP(controller),
		}
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	timeoutCtx, timeoutCancel := context.WithTimeoutCause(ctx, timeout,
		fmt.Errorf("no GABP server became available within %s", timeout))
	defer timeoutCancel()

	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)

		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeoutCtx.Done():
				return
			case <-ticker.C:
				if !controllerLooksAliveForMCP(controller) {
					cancel(fmt.Errorf("game process exited before GABP became available"))
					return
				}
			}
		}
	}()

	err := connector.AttemptConnection(timeoutCtx, gameID, endpoint.Port, endpoint.Token)
	timeoutCancel()
	<-monitorDone

	gameStillRunning := controllerLooksAliveForMCP(controller)
	result := startupConnectResult{
		Connected:        err == nil,
		Error:            err,
		Wait:             time.Since(startedAt),
		GameStillRunning: gameStillRunning,
	}
	if err != nil && !gameStillRunning {
		result.ProcessExitedDuringGABP = true
	}
	return result
}

func (s *Server) continueStartupGABPConnection(
	game config.GameConfig,
	controller process.ControllerInterface,
	endpoint bridgeEndpoint,
	backoffMin, backoffMax time.Duration,
	timeout time.Duration,
) {
	if timeout <= 0 {
		return
	}

	if !s.admitBackgroundTask() {
		return // shutting down
	}
	go func() {
		defer s.bgWG.Done()
		if client, _ := s.claimBoundClient(game.ID); client != nil {
			return
		}

		connector := NewAsyncServerGABPConnector(s, backoffMin, backoffMax)
		result := s.attemptStartupGABPConnection(controller, connector, game.ID, endpoint, timeout)
		if result.Connected {
			s.log.Infow("background GABP connection established",
				"gameId", game.ID,
				"port", endpoint.Port,
				"wait", result.Wait)
			return
		}
		if result.ProcessExitedDuringGABP {
			s.log.Warnw("background GABP connection stopped because game exited",
				"gameId", game.ID,
				"port", endpoint.Port,
				"error", result.Error)
			return
		}
		s.log.Warnw("background GABP connection timed out",
			"gameId", game.ID,
			"port", endpoint.Port,
			"wait", result.Wait,
			"error", result.Error)
	}()
}

func controllerLooksAliveForMCP(controller process.ControllerInterface) bool {
	if controller == nil {
		return false
	}
	if controller.IsRunning() {
		return true
	}
	switch controller.GetLaunchMode() {
	case "SteamAppId", "EpicAppId":
		return controller.IsLauncherProcessRunning()
	default:
		return false
	}
}

func resolveRuntimeGamePID(game config.GameConfig, controller process.ControllerInterface) int {
	if controller == nil {
		return 0
	}
	if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
		if game.StopProcessName != "" {
			pids, err := process.FindProcessesByName(game.StopProcessName)
			if err == nil && len(pids) > 0 {
				return pids[0]
			}
		}
		return 0
	}
	return controller.GetPID()
}

// checkGameStatus snapshots the in-memory state under s.mu, then evaluates
// evidence — including pinned status hooks that may run for seconds — with
// the lock released, so concurrent status calls never serialize behind one
// another's probes (design/10).
//
// A current-schema claim is the FIRST status authority (design/04): its
// pinned context is evaluated through the unified liveness rule before any
// in-memory shortcut — a live wrapper PID cannot hide a pinned hook's
// stopped verdict, a lingering client cannot masquerade as evidence, and
// M2.7 recovery is reachable regardless of controller state. GABP evidence
// comes only from a credential-bound live client: one this server attached
// under this claim's launch identity.
func (s *Server) checkGameStatus(gameID string) string {
	status, _ := s.checkGameStatusObserved(gameID)
	return status
}

// checkGameStatusObserved is checkGameStatus plus the liveness observation
// backing the verdict, for the status handlers to render (design/04).
func (s *Server) checkGameStatusObserved(gameID string) (string, *process.LivenessEvidence) {
	s.mu.Lock()
	controller, exists := s.games[gameID]
	client, clientConnected := s.gabpClients[gameID]
	attachRef, hasAttachRef := s.bridgeAttachments[gameID]
	s.mu.Unlock()

	gabpLive := clientConnected && client != nil && client.IsConnected()

	claim, err := process.LoadRuntimeState(gameID, s.configDir)
	if err != nil {
		s.log.Warnw("failed to read runtime claim for status", "gameId", gameID, "error", err)
		return "unknown", nil // uncertainty never cleans state
	}
	if claim != nil && claim.SchemaVersion >= process.RuntimeSchemaVersion {
		boundGABP := gabpLive && hasAttachRef && attachRef.launchID == claim.LaunchID
		status, ev := s.resolveClaimStatusObserved(gameID, claim, boundGABP)
		switch status {
		case process.RuntimeStateStatusRunning:
			// Preserve the existing rendering vocabulary on top of the
			// claim verdict.
			if boundGABP {
				if exists {
					return "running", ev
				}
				return "connected", ev
			}
			if clientConnected && !gabpLive {
				return "running-disconnected", ev
			}
			if exists {
				return "running", ev
			}
			return "shared-running", ev
		case "stale-runtime-cleaned":
			// The fenced removal accepted the stopped verdict: release
			// exactly the in-memory artifacts observed for that launch.
			s.releaseGameArtifacts(gameID, controller, client)
			return status, ev
		default:
			return status, ev // starting/stopping/killing/unknown
		}
	}

	// Legacy flows only from here: no claim, or a pre-profile claim that no
	// lifecycle touch has normalized yet.
	if !exists {
		if clientConnected {
			if gabpLive {
				return "connected", nil
			}
			return "disconnected", nil
		}
		if status := s.resolveSharedRuntimeStatus(gameID, gabpLive); status != "" {
			if status == process.RuntimeStateStatusRunning {
				return "shared-running", nil
			}
			return status, nil
		}
		return "stopped", nil
	}

	// Simple stateless approach: directly query the system state
	launchMode := controller.GetLaunchMode()

	// For Steam/Epic launcher games, check the actual game process
	if launchMode == "SteamAppId" || launchMode == "EpicAppId" {
		if controller.IsRunning() {
			if clientConnected && !gabpLive {
				return "running-disconnected", nil
			}
			return "running", nil // We can track it and it's running
		}
		// Check if the launcher process is still active
		if controller.IsLauncherProcessRunning() {
			return "launcher-running", nil // Launcher process is still active
		}

		game := s.getGameFromController(controller)
		if game != nil && game.StopProcessName != "" {
			// We have tracking capability but game is not running
			s.cleanupStoppedGame(gameID)
			return "stopped", nil
		}
		// We don't have tracking capability, so we can't know the real status
		return "launcher-triggered", nil // We started the launcher, but can't track the game
	}

	// For direct processes, check if the process is actually running
	if controller.IsRunning() {
		if clientConnected && !gabpLive {
			return "running-disconnected", nil
		}
		return "running", nil
	}

	// Process is dead, clean up (legacy tracking only — a current-schema
	// claim never reaches here).
	s.cleanupStoppedGame(gameID)
	return "stopped", nil
}

// cleanupStoppedGameLocked centralizes cleanup when s.mu is already held.
// It returns the popped attachment reference for the caller to clear after
// releasing s.mu (review round 9: no transition-lock acquisition under
// s.mu).
func (s *Server) cleanupStoppedGameLocked(gameID string) (bridgeAttachmentRef, bool) {
	// Remove from games map - no need for complex cleanup in stateless approach
	delete(s.games, gameID)

	// Note: The mutex is already held when this is called from checkGameStatus
	// So we call internal cleanup methods that don't acquire locks
	ref, hadRef := s.cleanupGABPConnectionInternal(gameID)
	s.cleanupGameResourcesInternal(gameID)
	s.cleanupRuntimeStateInternal(gameID)
	s.log.Debugw("cleaned up dead game process and resources", "gameId", gameID)
	return ref, hadRef
}

func (s *Server) cleanupStoppedGame(gameID string) {
	s.mu.Lock()
	ref, hadRef := s.cleanupStoppedGameLocked(gameID)
	s.mu.Unlock()
	if hadRef {
		s.clearBridgeAttachment(gameID, ref.launchID, ref.connectionID)
	}
}

// startGame starts a game process using the serialized starter approach
// This implements @pardeike's requirements for serialized, verified process starting
// processStartBudget is the Stage 4 verification budget (design/05):
// URL modes get longer because the store launcher sits in the middle.
func processStartBudget(mode string) time.Duration {
	if isURLMode(mode) {
		return 60 * time.Second
	}
	return 10 * time.Second
}

// startBudgetFor returns the single process-start budget used for BOTH the
// claim's pinned deadlines and the starter's verification wait — a claim
// deadline that outlives (or undercuts) the actual wait enables incorrect
// concurrent gating and supersession. Explicit config wins for all modes.
func (s *Server) startBudgetFor(mode string) time.Duration {
	if s.gamesConfig != nil && s.gamesConfig.Timeouts != nil &&
		s.gamesConfig.Timeouts.Startup != nil && s.gamesConfig.Timeouts.Startup.ProcessStartSeconds > 0 {
		return time.Duration(s.gamesConfig.Timeouts.Startup.ProcessStartSeconds) * time.Second
	}
	return processStartBudget(mode)
}

func isURLMode(mode string) bool { return mode == "SteamAppId" || mode == "EpicAppId" }

func (s *Server) hasLiveGABPClient(gameID string) bool {
	// GABP evidence requires a claim-bound client (review round 8): a live
	// socket alone — possibly belonging to an earlier launch — is never
	// proof about the CURRENT claim.
	client, _ := s.claimBoundClient(gameID)
	return client != nil
}

// stampSpawnState applies a fenced claim mutation; a fencing violation
// means the claim was superseded mid-start and is only logged — the OS
// spawn itself is not abortable from here.
func (s *Server) stampSpawnState(gameID, launchID, opID string, mutate func(*process.RuntimeState)) {
	if _, err := process.FencedTransition(gameID, s.configDir, launchID, opID, func(st *process.RuntimeState) error {
		mutate(st)
		return nil
	}); err != nil {
		s.log.Warnw("spawn-state transition not applied", "gameId", gameID, "error", err)
	}
}

func (s *Server) startGame(game config.GameConfig, gamesConfig *config.GamesConfig, backoffMin, backoffMax time.Duration, startupGABPTimeout time.Duration, resetEndpoint bool, resolved *launch.Resolved, hc historyContext) (*process.ProcessStartResult, error) {
	launchSpec := s.launchSpecWithRuntimeDir(launchSpecFromResolved(game, resolved))

	controller := s.newController()
	if err := controller.Configure(launchSpec); err != nil {
		return nil, fmt.Errorf("failed to configure game launcher for '%s' (mode: %s, target: %s): %w",
			game.ID, game.LaunchMode, game.Target, err)
	}

	// Stage 2 (design/05): claim gating by the liveness rule, complete
	// pre-spawn claim with operation stamping, then all-profile probing +
	// stopProcessName as the lost-claim backstop.
	startBudget := s.startBudgetFor(game.LaunchMode)
	gateRes, err := process.GateStart(process.StartGate{
		GameID:             game.ID,
		ConfigDir:          s.configDir,
		InstanceID:         s.instanceID,
		RequestedProfile:   launchSpec.Profile,
		BridgeBound:        s.bridgeBound(game.ID),
		Spec:               launchSpec,
		Budget:             startBudget,
		Probes:             launch.ResolveProfileLifecycles(&game),
		StopProcessName:    game.StopProcessName,
		HistoryContextHash: hc.contextHash,
		HistorySuccess:     &process.HistorySuccessIdentity{Snapshot: hc.snapshot, Bucket: hc.bucket},
	})
	if err != nil {
		return nil, err
	}
	if gateRes.Refusal != nil {
		return nil, &startRefusalError{refusal: gateRes.Refusal, warnings: gateRes.Warnings}
	}
	runtimeState := *gateRes.Claim
	launchID := runtimeState.LaunchID
	opID := runtimeState.Operation.OperationID
	startWarnings := gateRes.Warnings
	hc.launchID = launchID

	cleanupRuntimeState := true
	// Terminal accepted-attempt failures must be written to history while the
	// claim is still alive and fenced to THIS launch (round 10): the handler's
	// renderer runs after the deferred release, so the write cannot live there.
	// exitedFailure records inline (its claim is torn down mid-flow); the other
	// cleanup-path codes record here, immediately before the release.
	failureRecorded := false
	var pendingFailCode string
	recordFail := func(code string) {
		if failureRecorded || code == "" {
			return
		}
		failureRecorded = true
		s.recordTerminalStartFailure(game, hc, code)
	}
	defer func() {
		if !cleanupRuntimeState {
			return
		}
		recordFail(pendingFailCode)
		// Release only OUR claim: fenced by the launch + operation identity
		// this start created — a cleanup that lost a race (its transition
		// fenced out, its claim superseded) must never delete a successor
		// claim by bare game ID (design/06).
		if err := process.ReleaseStartClaim(game.ID, s.configDir, s.instanceID, launchID, opID, s.selfConnectionFor(game.ID, launchID)); err != nil &&
			!errors.Is(err, process.ErrFencingViolation) && !errors.Is(err, process.ErrNoRuntimeClaim) {
			s.log.Warnw("failed to release start claim", "gameId", game.ID, "error", err)
		}
	}()

	s.mu.Lock()
	if trackedController, exists := s.games[game.ID]; exists && trackedController != nil && trackedController.IsRunning() {
		s.mu.Unlock()
		return nil, &gameAlreadyActiveError{status: "running"}
	}

	// Clean up any stale controller reference
	delete(s.games, game.ID)
	s.mu.Unlock()

	// portRanges is startup-only (design/09): endpoint allocation uses the
	// configuration pinned at process start, never the hot-reload snapshot.
	// Failure is structured and the claim is released (deferred cleanup).
	// This writes only the endpoint (port/token); the diagnostic fields are
	// stamped at the spawn boundary (design/20), so a pre-spawn failure never
	// leaves a profile/revision/startedAt for a process never spawned.
	port, token, bridgePath, reusedBridge, err := config.PrepareBridgeEndpointForStart(game.ID, s.configDir, s.gamesConfig, resetEndpoint)
	if err != nil {
		pendingFailCode = "endpoint_unavailable"
		return nil, &endpointUnavailableError{gameID: game.ID, err: err}
	}

	if reusedBridge {
		s.log.Infow("reusing GABS endpoint cache", "gameId", game.ID, "port", port, "host", "127.0.0.1", "configPath", bridgePath)
	} else {
		s.log.Infow("created GABS endpoint cache", "gameId", game.ID, "port", port, "host", "127.0.0.1", "configPath", bridgePath, "resetEndpoint", resetEndpoint)
	}

	controller.SetBridgeInfo(port, token)

	// The claim carries the endpoint (port + per-launch token): it is the
	// normal attachment source for games_connect after a CLI start or a
	// server restart (design/07). The expected-context digests are pinned
	// in the same transition — non-reversible salted hashes of the argv
	// payload, canonical cwd, and forwarded env values (design/03), so a
	// delayed welcome report verifies against what was actually spawned,
	// never current config.
	spawnDigests := computeSpawnDigests(launchSpec, controller)
	if _, err := process.FencedTransition(game.ID, s.configDir, launchID, opID, func(st *process.RuntimeState) error {
		st.Endpoint = &process.RuntimeEndpoint{Port: port, Token: token}
		st.ContextDigests = spawnDigests
		// HistoryContextHash + HistorySuccess were pinned at claim publication
		// (GateStart), so a crash before this transition still leaves recovery
		// a track-record identity (round 11 P1-2).
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to persist endpoint into runtime claim for '%s': %w", game.ID, err)
	}

	// Pre-spawn size check on the fully materialized spec (managed env
	// layer + executable included): a structured spec_too_large beats an
	// opaque E2BIG/CreateProcess failure (design/03).
	if resolved != nil {
		finalEnv := map[string]string{}
		for _, kv := range controller.FinalEnvironment() {
			if i := strings.IndexByte(kv, '='); i > 0 {
				finalEnv[kv[:i]] = kv[i+1:]
			}
		}
		finalArgv := append([]string{launchSpec.PathOrId}, launchSpec.Args...)
		if iss := launch.CheckProcessSize(finalArgv, finalEnv); iss != nil {
			pendingFailCode = "spec_too_large"
			return nil, iss
		}
	}

	// A diagnostic-stamp write failure surfaced from the spawn observer
	// (F10). Guarded because the observer may run on the spawning goroutine.
	var stampMu sync.Mutex
	var stampWarnings []string

	// Stage 3: spawnState transitions bracket OS process creation. The
	// pre-spawn transition must succeed — spawning against an unpublished
	// or superseded claim would orphan a real workload behind a claim that
	// recovery considers safely removable.
	controller.SetSpawnObservers(
		func() error {
			_, terr := process.FencedTransition(game.ID, s.configDir, launchID, opID, func(st *process.RuntimeState) error {
				st.SpawnState = process.SpawnStateSpawning
				return nil
			})
			return terr
		},
		func(pid int, startTime int64, spawnErr error) {
			s.stampSpawnState(game.ID, launchID, opID, func(st *process.RuntimeState) {
				if spawnErr != nil {
					st.SpawnState = process.SpawnStateFailed
					return
				}
				st.SpawnState = process.SpawnStateSpawned
				st.GamePID = pid
				st.PIDStartTime = startTime
			})
			// Stamp the diagnostic-only bridge.json fields at the spawn
			// boundary (design/20: "written at spawn"), ONLY on a successful
			// spawn (round 11 P2-8) and FENCED to this launch's endpoint —
			// passing the expected port+token so a superseded launch never
			// writes onto the successor's rotated token (round 12 F10).
			if spawnErr == nil {
				err := config.StampBridgeDiagnostics(game.ID, s.configDir, port, token, config.BridgeDiagnostics{
					Profile:        launchSpec.Profile,
					ConfigRevision: launchSpec.ConfigRevision,
					StartedAt:      time.Now().UTC().Format(time.RFC3339),
				})
				switch {
				case err == nil:
				case errors.Is(err, config.ErrBridgeEndpointRotated):
					// Expected: a successor took the endpoint; the fence
					// correctly skipped this launch's stamp.
					s.log.Debugw("skipped bridge diagnostics stamp on rotated endpoint", "gameId", game.ID)
				default:
					// A write failure left a spawned launch without diagnostics
					// — surface it, don't just log (round 12 F10).
					stampMu.Lock()
					stampWarnings = append(stampWarnings, fmt.Sprintf("bridge diagnostics could not be written for this launch: %v", err))
					stampMu.Unlock()
					s.log.Warnw("failed to stamp bridge diagnostics", "gameId", game.ID, "error", err)
				}
			}
		})

	result := s.starter.StartWithVerificationWithTimeouts(controller, nil, game.ID, port, token, startBudget, 0)
	// Merge any diagnostic-stamp warning the spawn observer surfaced (F10).
	stampMu.Lock()
	startWarnings = append(startWarnings, stampWarnings...)
	stampMu.Unlock()
	if result != nil {
		result.StartWarnings = startWarnings
	}

	// assessWorkload runs Stage 4 over the unified, pinned liveness
	// sources (design/05): the claim's status hook and built-in evidence —
	// never just the direct PID and current-config name.
	assessWorkload := func() process.LivenessEvidence {
		claim, _ := process.LoadRuntimeState(game.ID, s.configDir)
		if claim == nil {
			claim = &runtimeState
		}
		var hook *launch.ResolvedHook
		if launchSpec.Lifecycle != nil {
			hook = launchSpec.Lifecycle.Status
		}
		return process.EvaluateLiveness(process.LivenessInput{
			CallerInstanceID: s.instanceID,
			Claim:            claim,
			StatusHook:       hook,
			GameID:           game.ID,
			Profile:          launchSpec.Profile,
		})
	}
	// keepClaimUnobserved finalizes the Stage 4 unobserved outcome: the
	// claim is KEPT in phase starting; a later observation promotes it,
	// the supersession policy or repair clears it — never a no-evidence
	// status pass (design/05).
	keepClaimUnobserved := func() (*process.ProcessStartResult, error) {
		cleanupRuntimeState = false
		// The unobserved attempt is a terminal failure of an accepted attempt
		// (design/20:206): proof-adjusted class (proven→environment, never-
		// proven→config). Computed before the transition to avoid nesting a
		// history read inside the history write.
		unobservedClass := process.Classify("unobserved", process.ClassifyContext{Proven: s.contextProven(game.ID, hc)}).Class
		if _, ferr := process.FencedTransition(game.ID, s.configDir, launchID, opID, func(st *process.RuntimeState) error {
			st.Operation = nil // the attempt is over; the deadline governs reclaim
			// Record the unobserved failure fenced to this still-present claim
			// (round 11 P2-3): repeated unobserved launches accumulate
			// consecutiveFailures; a later passive Stage 4 promotion resets them
			// through the pinned success update.
			if st.HistoryContextHash != "" {
				process.ApplyActionFailureLocked(game.ID, s.configDir, process.EffectiveClaimProfile(st), st.HistoryContextHash, "unobserved", unobservedClass, hc.inputNames, time.Now().UTC())
			}
			return nil
		}); ferr != nil {
			// A stable Stage 4 outcome is emitted only after its transition
			// lands (design/05; review rounds 8-9): a fencing loss reports
			// supersession (re-evaluated to a stable code), a persistence
			// failure keeps the claim occupied with blocked_unknown_state.
			if errors.Is(ferr, process.ErrFencingViolation) || errors.Is(ferr, process.ErrNoRuntimeClaim) {
				return result, s.supersededStartRefusal(game.ID)
			}
			return result, occupiedClaimRefusal(game.ID, "the unobserved outcome could not be persisted", ferr)
		}
		return result, &unobservedStartError{warnings: startWarnings}
	}
	exitedFailure := func(ev *process.LivenessEvidence) (*process.ProcessStartResult, error) {
		// Record while the claim is still ours (round 10): some exited paths
		// remove the claim right after this returns, so the fenced write must
		// land here, not in the handler's post-release renderer.
		// exited_during_start is ALWAYS game (the evidence-based default,
		// design/05 F6 adjudication): the status hook here is liveness, not
		// cause — it only enriches the captured output (hookEvidence), never
		// the class. GABS cannot tell a game crash from a wrapper/container
		// exit at the first process it created.
		hookStopped := ev != nil && ev.Source == process.LivenessSourceStatusHook
		recordFail("exited_during_start")
		e := &exitedDuringStartError{
			exitCode:            controller.ExitCode(),
			tail:                controller.LaunchLogTail(16 * 1024),
			warnings:            startWarnings,
			hookReportedStopped: hookStopped,
		}
		if hookStopped {
			e.hookEvidence = ev.Detail
		}
		return result, e
	}

	if result.Error != nil {
		var procErr *process.ProcessError
		if errors.As(result.Error, &procErr) && procErr.Type == process.ProcessErrorTypeUnobserved {
			ev := assessWorkload()
			switch ev.Verdict {
			case process.StatusRunning:
				// the workload is observable (hook or name) even though the
				// starter's own tracking gave up — verified, likely adopted
				result.Error = nil
				result.ProcessStarted = true
				result.GameStillRunning = true
			case process.StatusStopped:
				if ev.Source == process.LivenessSourceStatusHook {
					return exitedFailure(&ev) // stopped-by-hook after spawn
				}
				return keepClaimUnobserved() // absence, not positive evidence
			default:
				return keepClaimUnobserved()
			}
		} else if errors.Is(result.Error, process.ErrFencingViolation) {
			cleanupRuntimeState = false
			return result, s.supersededStartRefusal(game.ID)
		} else {
			// Record spawn_failed ONLY when the handler will also render it as
			// spawn_failed (round 10): a Start/Configuration ProcessError. Any
			// other error type falls through to the handler's generic branch
			// with no causeClass — writing spawn_failed for it would credit a
			// history failure the caller never sees classified.
			if procErr != nil && (procErr.Type == process.ProcessErrorTypeStart || procErr.Type == process.ProcessErrorTypeConfiguration) {
				pendingFailCode = "spawn_failed"
			}
			return result, fmt.Errorf("failed to start game '%s' (mode: %s, target: %s): %w",
				game.ID, game.LaunchMode, game.Target, result.Error)
		}
	}
	if !result.GameStillRunning {
		ev := assessWorkload()
		switch ev.Verdict {
		case process.StatusRunning:
			result.GameStillRunning = true // adopted: workload observed
		case process.StatusUnknown:
			return keepClaimUnobserved() // nothing observable is not an exit
		default:
			return exitedFailure(&ev)
		}
	}

	// Adoption (design/05 Stage 4): defined by the direct child exiting
	// while the workload stays observable — wrappers on ANY mode cross
	// exactly the boundary where injected args/env can be lost.
	adopted := controller.DirectChildExited()
	result.Adopted = adopted

	_, defaultGABPTimeout := s.starter.GetTimeouts()
	totalGABPTimeout := startupGABPTimeout
	if totalGABPTimeout <= 0 {
		totalGABPTimeout = defaultGABPTimeout
	}
	newPID := resolveRuntimeGamePID(game, controller)
	wasStarting := false
	promoted, err := process.FencedTransitionThen(game.ID, s.configDir, launchID, opID, func(st *process.RuntimeState) error {
		wasStarting = st.Phase == process.PhaseStarting
		st.Status = process.RuntimeStateStatusRunning
		st.Phase = process.PhaseActive
		st.GamePID = newPID
		if fp, ferr := process.ProcessStartTime(newPID); ferr == nil {
			st.PIDStartTime = fp
		}
		st.Adopted = adopted
		st.Operation = nil
		st.ProcessStartDeadline = time.Time{}
		*st = process.RefreshRuntimeOwnerLease(*st, os.Getpid(), s.instanceID, s.runtimeOwnerLeaseForOperation(totalGABPTimeout), time.Now().UTC())
		return nil
	}, func(st *process.RuntimeState) {
		// Stage 4 verified: credit workloadStarts++ AFTER the flip commits
		// (round 11 P1-2; round 13 F5), only when this transition actually
		// promoted a starting claim, so the four promotion paths record once.
		if wasStarting {
			s.applyPinnedWorkloadStart(game.ID, st)
		}
	})
	if err != nil {
		cleanupRuntimeState = false
		if errors.Is(err, process.ErrFencingViolation) || errors.Is(err, process.ErrNoRuntimeClaim) {
			return result, s.supersededStartRefusal(game.ID)
		}
		// A started_* outcome is emitted only after the promote transition
		// lands: the claim stays occupied (operation in place) and the
		// caller gets a stable blocked_unknown_state, not an unclassified
		// error (review round 9).
		return result, occupiedClaimRefusal(game.ID, "the running state could not be persisted", err)
	} else if promoted != nil {
		runtimeState = *promoted
	}
	cleanupRuntimeState = false

	// Stage 4 verified was recorded INSIDE the promote fence above (round 11
	// P1-2), so the passive promotion paths (status, attachment, recovery)
	// never double-count and a crash between promote and record cannot lose it.

	s.mu.Lock()
	s.games[game.ID] = controller
	s.mu.Unlock()

	// The claim's per-launch credential is the ONLY credential this launch
	// may attach with (design/03). Process-environment inspection can
	// corroborate it — or expose that the observed workload still carries a
	// previous launch's environment (an adopted process that never received
	// this launch's context): that is stale_bridge_credential, surfaced as
	// a warning, never adopted as an attach credential.
	endpoint := bridgeEndpoint{Port: port, Token: token, Source: "runtime-claim"}
	adoptedProcessEnv := false
	if processEnv := s.inspectGameBridgeEnvironment(game, &runtimeState); processEnv.Present {
		switch {
		case processEnv.Port == port && processEnv.Token == token:
			endpoint.Source = "process-environment"
			endpoint.PID = processEnv.PID
		case processEnv.Port > 0 && strings.TrimSpace(processEnv.Token) != "":
			startWarnings = append(startWarnings, fmt.Sprintf(
				"stale_bridge_credential: the running workload (pid %d) carries bridge credentials from a previous launch environment; this launch's bridge cannot attach to it — restart the game to pick up the new environment",
				processEnv.PID))
			result.StartWarnings = startWarnings
		}
	}

	synchronousGABPTimeout := boundedStartupGABPWait(totalGABPTimeout)
	connector := NewAsyncServerGABPConnector(s, backoffMin, backoffMax)
	connectResult := s.attemptStartupGABPConnection(controller, connector, game.ID, endpoint, synchronousGABPTimeout)
	result.GABPConnected = connectResult.Connected
	result.GABPConnectError = connectResult.Error
	result.GABPConnectWait = connectResult.Wait
	result.GameStillRunning = connectResult.GameStillRunning
	result.ProcessExitedDuringGABP = connectResult.ProcessExitedDuringGABP
	// bridgeConnects++ is recorded at credential-bound attachment
	// publication (recordBridgeAttachment), covering the synchronous,
	// background, reconnect, and migration paths exactly once — not here.

	if !result.GameStillRunning {
		// Stage 5: the workload looks dead while waiting for the bridge —
		// judged by the pinned liveness rule: proven death fails with exit
		// evidence and clears the claim; unknown keeps the claim and falls
		// through as bridge-pending (design/05).
		ev := assessWorkload()
		switch ev.Verdict {
		case process.StatusRunning:
			result.GameStillRunning = true
			result.Adopted = result.Adopted || controller.DirectChildExited()
		case process.StatusStopped:
			// Record the terminal failure while the claim is still ours, THEN
			// remove it (round 10): the fenced write must see our launchID.
			res, ferr := exitedFailure(&ev)
			// The promote transition already cleared our operation, so this
			// completion makes its own fully fenced decision (design/06):
			// same launch, still no operation, no removal-blocking
			// attachment — never the generic operation-fenced defer.
			if rerr := process.RemoveRuntimeStateIfCurrent(game.ID, s.configDir, s.instanceID, launchID, s.selfConnectionFor(game.ID, launchID)); rerr != nil {
				if !errors.Is(rerr, process.ErrFencingViolation) {
					s.log.Warnw("failed to release exited launch claim", "gameId", game.ID, "error", rerr)
				}
				result.StartWarnings = append(result.StartWarnings, "the exited launch's claim was superseded or held and was left in place")
			}
			return res, ferr
		default:
			// unknown never cleans state; status/connect will resolve it
		}
	}

	if !result.GABPConnected {
		remaining := remainingStartupGABPWait(totalGABPTimeout, result.GABPConnectWait)
		if remaining > 0 {
			result.BackgroundGABPConnect = true
			result.BackgroundGABPWait = remaining
			s.continueStartupGABPConnection(game, controller, endpoint, backoffMin, backoffMax, remaining)
		}
	}

	logMsg := fmt.Sprintf("game started with GABP bridge (pid: %d, port: %d)", controller.GetPID(), port)
	if result.ProcessStarted {
		logMsg += ", process verified"
	}
	if result.GABPConnected {
		logMsg += ", GABP connected"
	} else {
		logMsg += ", GABP not ready yet"
	}

	s.log.Infow(logMsg,
		"gameId", game.ID,
		"mode", game.LaunchMode,
		"processStarted", result.ProcessStarted,
		"gabpConnected", result.GABPConnected,
		"gabpWait", result.GABPConnectWait,
		"gabpError", result.GABPConnectError,
		"bridgeEndpointSource", endpoint.Source,
		"adoptedProcessEnvironment", adoptedProcessEnv,
		"totalGABPTimeout", totalGABPTimeout,
		"synchronousGABPTimeout", synchronousGABPTimeout)

	return result, nil
}

// establishGABPConnection attempts to connect to the game's GABP server with retry logic.
// This runs in the background and implements the game-development workflow:
//  1. Game starts with bridge config (already done in startGame)
//  2. GABP client connects to the game's GABP server (implemented here)
//  3. Mirror system syncs tools into the stable games_tool_names discovery path
//  4. AI agents discover capabilities via games_tool_names, then inspect a few
//     candidates with games_tool_detail before calling games_call_tool
func (s *Server) establishGABPConnection(gameID string, port int, token string, backoffMin, backoffMax time.Duration) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	s.log.Debugw("attempting GABP connection for game", "gameId", gameID, "addr", addr)

	// Create GABP client
	client := gabp.NewClient(s.log)

	// Store client reference for cleanup
	s.mu.Lock()
	s.gabpClients[gameID] = client
	s.mu.Unlock()

	// Attempt connection with retry logic (handles game bridge startup delays)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err := client.Connect(ctx, addr, token, backoffMin, backoffMax)
	if err != nil {
		s.log.Warnw("failed to establish GABP connection - game may not support GABP",
			"gameId", gameID, "addr", addr, "error", err)

		// Clean up client reference on failure
		s.mu.Lock()
		delete(s.gabpClients, gameID)
		s.mu.Unlock()
		return
	}

	s.log.Infow("GABP connection established successfully", "gameId", gameID, "addr", addr)

	// Sync tools from GABP to MCP (inline mirroring logic)
	if err := s.syncGABPTools(client, gameID); err != nil {
		s.log.Warnw("failed to sync GABP tools", "gameId", gameID, "error", err)
	} else {
		s.log.Infow("GABP tools synchronized successfully", "gameId", gameID)
	}

	// Expose GABP resources as MCP resources (inline mirroring logic)
	if err := s.exposeGABPResources(client, gameID); err != nil {
		s.log.Warnw("failed to expose GABP resources", "gameId", gameID, "error", err)
	} else {
		s.log.Infow("GABP resources exposed successfully", "gameId", gameID)
	}

	s.log.Infow("GABP mirroring setup complete for game", "gameId", gameID)
}

// syncGABPTools mirrors GABP tools to MCP tools with game-specific naming
func (s *Server) syncGABPTools(client *gabp.Client, gameID string) error {
	return s.syncGABPToolsWithTimeout(client, gameID, 30*time.Second)
}

func (s *Server) syncGABPToolsWithTimeout(client *gabp.Client, gameID string, timeout time.Duration) error {
	// Get tools from GABP client
	gabpTools, err := client.ListToolsWithTimeout(timeout)
	if err != nil {
		return fmt.Errorf("failed to list GABP tools: %w", err)
	}

	// Register each GABP tool as an MCP tool with game-specific naming
	for _, tool := range gabpTools {
		rawGABPToolName := strings.TrimSpace(tool.Name)
		gabpToolName := canonicalGABPToolName(rawGABPToolName)
		if gabpToolName == "" {
			continue
		}
		exposedToolName := s.safeMCPToolNameForGABPTool(gameID, gabpToolName)
		legacyToolName := legacyMCPToolName(gameID, gabpToolName)
		qualifiedToolName := qualifiedGABPToolName(gameID, gabpToolName)

		meta := map[string]interface{}{
			toolMetaGABPName:          gabpToolName,
			toolMetaQualifiedGABPName: qualifiedToolName,
			toolMetaLegacyName:        legacyToolName,
			toolMetaAliases:           []string{legacyToolName, qualifiedToolName, localLegacyMCPToolName(gabpToolName), gabpToolName, rawGABPToolName},
			"originalName":            legacyToolName,
		}
		if len(tool.Tags) > 0 {
			meta[toolMetaTags] = append([]string(nil), tool.Tags...)
		}

		mcpTool := Tool{
			Name:         exposedToolName,
			Description:  fmt.Sprintf("%s (Game: %s)", tool.Description, gameID),
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
			Meta:         meta,
		}

		handler := func(toolName, exposedName string) func(args map[string]interface{}) (*ToolResult, error) {
			return func(args map[string]interface{}) (*ToolResult, error) {
				proxyTimeout, invalidTimeout := deriveMirroredToolCallTimeout(args, 30*time.Second)
				if invalidTimeout != nil {
					return invalidTimeout, nil
				}

				if blocked := s.ensureRuntimeOwnershipForGameCall(gameID, fmt.Sprintf("tool '%s'", exposedName), proxyTimeout); blocked != nil {
					return blocked, nil
				}

				// Resolve the CURRENT claim-bound client at invocation
				// time: handlers must never retain the discovery-time
				// client — a reconnect replaces the connection while the
				// mirrored tools remain installed (review round 9).
				liveClient, _ := s.claimBoundClient(gameID)
				if liveClient == nil {
					return &ToolResult{
						Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' is not connected via GABP. Use games_status to verify whether it is still running, then use games_connect or games_start as appropriate.", gameID)}},
						IsError: true,
					}, nil
				}

				if !shouldBypassAttentionGateForTool(mcpTool, exposedName, toolName) {
					if blocked := s.enforceAttentionGate(gameID, exposedName, liveClient); blocked != nil {
						return blocked, nil
					}
				}

				// Call GABP with original tool name (without game prefix)
				result, isError, err := liveClient.CallToolWithTimeout(toolName, args, proxyTimeout)
				if err != nil {
					return &ToolResult{
						Content: []Content{{Type: "text", Text: err.Error()}},
						IsError: true,
					}, nil
				}

				if isError {
					return &ToolResult{
						Content:           []Content{{Type: "text", Text: fmt.Sprintf("Tool error: %v", result)}},
						StructuredContent: result,
						IsError:           true,
					}, nil
				}

				// Convert result to MCP format
				content := []Content{}
				if resultText, ok := result["text"].(string); ok {
					content = append(content, Content{Type: "text", Text: resultText})
				} else {
					// Serialize non-text tool results as JSON instead of using %v
					if jsonData, err := json.Marshal(result); err != nil {
						// Fallback to string representation if JSON marshaling fails
						content = append(content, Content{Type: "text", Text: fmt.Sprintf("Tool result (JSON marshal failed): %v", result)})
					} else {
						content = append(content, Content{Type: "text", Text: string(jsonData)})
					}
				}

				return &ToolResult{
					Content:           content,
					StructuredContent: result,
					IsError:           false,
				}, nil
			}
		}(gabpToolName, exposedToolName)

		normalizationConfig := &config.ToolNormalizationConfig{}
		s.RegisterGameTool(gameID, mcpTool, handler, normalizationConfig)
		s.log.Debugw("registered GABP tool as game-specific MCP tool", "gameId", gameID, "gabpName", gabpToolName, "mcpName", exposedToolName, "legacyName", legacyToolName)
	}

	s.log.Infow("synced GABP tools to MCP with game namespacing", "gameId", gameID, "count", len(gabpTools))

	return nil
}

// exposeGABPResources creates MCP resources that expose GABP game information
func (s *Server) exposeGABPResources(client *gabp.Client, gameID string) error {
	// Game state resource for exposing current game information
	stateResource := Resource{
		URI:         fmt.Sprintf("gab://%s/state", gameID),
		Name:        fmt.Sprintf("%s Game State", gameID),
		Description: fmt.Sprintf("Current state and capabilities of game: %s", gameID),
		MimeType:    "application/json",
	}

	stateHandler := func() ([]Content, error) {
		// Get current tools to show game capabilities
		tools, err := client.ListTools()
		if err != nil {
			return []Content{
				{Type: "text", Text: fmt.Sprintf("Error retrieving game state: %v", err)},
			}, nil
		}

		stateData := map[string]interface{}{
			"gameId":       gameID,
			"connected":    true,
			"toolCount":    len(tools),
			"capabilities": client.GetCapabilities(),
			"availableTools": func() []string {
				var toolNames []string
				for _, tool := range tools {
					toolNames = append(toolNames, tool.Name)
				}
				return toolNames
			}(),
			"lastUpdate": fmt.Sprintf("%d", time.Now().Unix()),
		}

		stateJson, err := json.Marshal(stateData)
		if err != nil {
			return []Content{
				{Type: "text", Text: fmt.Sprintf("Error marshaling state data: %v", err)},
			}, err
		}

		return []Content{
			{Type: "text", Text: string(stateJson)},
		}, nil
	}

	// Register the resource using the existing game resource registration method
	s.RegisterGameResource(gameID, stateResource, stateHandler)

	s.log.Infow("exposed GABP resources as game-specific MCP resources", "gameId", gameID, "resources", []string{"state"})

	// Send resources/list_changed notification to alert AI agents
	s.SendResourcesListChangedNotification()

	return nil
}

func launchSpecFromGame(game config.GameConfig) process.LaunchSpec {
	return process.LaunchSpec{
		GameId:          game.ID,
		Mode:            game.LaunchMode,
		PathOrId:        game.Target,
		Args:            game.Args,
		WorkingDir:      game.WorkingDir,
		StopProcessName: game.StopProcessName,
	}
}

func (s *Server) launchSpecWithRuntimeDir(spec process.LaunchSpec) process.LaunchSpec {
	if cp, err := config.NewConfigPaths(s.configDir); err == nil {
		spec.RuntimeDir = cp.GetGameDir(spec.GameId)
	}
	return spec
}

// launchSpecFromResolved builds the process spec from the resolver output:
// resolved args/env/cwd plus profile context, with macOS .app bundle targets
// resolved to their inner executable for propagation-capable modes.
func launchSpecFromResolved(game config.GameConfig, r *launch.Resolved) process.LaunchSpec {
	spec := launchSpecFromGame(game)
	// Bundle resolution applies to every propagation-capable path mode:
	// Stage 1 checks the inner executable, so the spawn must exec the same
	// effective target or a passing check would still spawn_fail.
	if game.LaunchMode == "DirectPath" || game.LaunchMode == "" || game.LaunchMode == "CustomCommand" {
		spec.PathOrId = launch.EffectiveDirectPathTarget(game.Target)
	}
	if r == nil {
		return spec
	}
	spec.Args = append([]string(nil), r.Args...)
	spec.WorkingDir = r.WorkingDir
	spec.Profile = r.Profile
	spec.Env = r.Env
	spec.ContextEnvKeys = append([]string(nil), r.ContextEnvKeys...)
	spec.AbsentEnvNames = append([]string(nil), r.AbsentEnvNames...)
	spec.AppliedInputs = append([]string(nil), r.AppliedInputs...)
	spec.ConfigRevision = r.ConfigRevision
	spec.Lifecycle = r.Lifecycle
	return spec
}

// stopGame stops a game process gracefully or by force
func (s *Server) stopGame(game config.GameConfig, force bool) error {
	s.mu.Lock()
	controller, exists := s.games[game.ID]
	if !exists {
		s.mu.Unlock()
		return s.stopUntrackedGame(game, force)
	}

	launchMode := controller.GetLaunchMode()

	// Remove from tracking immediately to prevent double-stops
	delete(s.games, game.ID)
	s.mu.Unlock()

	defer s.cleanupStoppedGame(game.ID)

	// Handle different launch modes differently
	if launchMode == "SteamAppId" || launchMode == "EpicAppId" {
		// For Steam/Epic games, try to use stopProcessName first if available
		if game.StopProcessName != "" {
			// Try to stop by process name first
			if err := controller.Stop(3 * time.Second); err == nil {
				s.log.Infow("game stopped via process name", "gameId", game.ID, "processName", game.StopProcessName)
				return nil
			}
		}

		// Fall back to stopping the launcher process
		var err error
		if force {
			err = controller.Kill()
		} else {
			err = controller.Stop(3 * time.Second)
		}

		if err != nil {
			s.log.Infow("launcher process stop failed (may have already exited)", "gameId", game.ID, "mode", launchMode, "error", err)
		} else {
			s.log.Infow("launcher process stopped", "gameId", game.ID, "mode", launchMode, "pid", controller.GetPID())
		}

		// If we have stopProcessName configured, we should have been able to stop the game properly
		if game.StopProcessName != "" {
			return nil // Process was handled by stopProcessName logic above
		}

		// Only show the confusing message if stopProcessName is not configured
		return fmt.Errorf("launcher process stopped, but the actual %s game may still be running independently. Configure 'stopProcessName' in the game configuration to enable proper game termination", launchMode)
	}

	// For direct processes, stop normally
	var err error
	if force {
		err = controller.Kill()
		s.log.Infow("game killed", "gameId", game.ID, "pid", controller.GetPID())
	} else {
		// Use default grace period of 3 seconds
		err = controller.Stop(3 * time.Second)
		s.log.Infow("game stopped", "gameId", game.ID, "pid", controller.GetPID())
	}

	return err
}

func (s *Server) stopUntrackedGame(game config.GameConfig, force bool) error {
	if game.StopProcessName == "" {
		return fmt.Errorf("game %s is not running (no process tracked)", game.ID)
	}

	controller := process.NewController()
	if err := controller.Configure(launchSpecFromGame(game)); err != nil {
		return fmt.Errorf("failed to configure fallback stop controller for %s: %w", game.ID, err)
	}

	if !controller.IsRunning() {
		return fmt.Errorf("game %s is not running (no process tracked; no process named %q found)", game.ID, game.StopProcessName)
	}

	var err error
	if force {
		err = controller.Kill()
	} else {
		err = controller.Stop(3 * time.Second)
	}
	if err != nil {
		return err
	}

	s.log.Infow("untracked game stopped via configured process name", "gameId", game.ID, "processName", game.StopProcessName, "force", force)
	s.cleanupStoppedGame(game.ID)
	return nil
}

func (s *Server) ServeStdio(ctx context.Context) error {
	return s.Serve(os.Stdin, os.Stdout)
}

// SendNotification sends a notification to all connected clients
func (s *Server) SendNotification(method string, params interface{}) {
	notification := NewNotification(method, params)

	s.writersMu.RLock()
	defer s.writersMu.RUnlock()

	for _, writer := range s.writers {
		if err := writer.WriteJSON(notification); err != nil {
			s.log.Warnw("failed to send notification", "method", method, "error", err)
		}
	}
}

// SendToolsListChangedNotification notifies clients that the tool list has changed
func (s *Server) SendToolsListChangedNotification() {
	s.SendNotification("notifications/tools/list_changed", map[string]interface{}{})
	s.log.Debugw("sent tools/list_changed notification")
}

// SendResourcesListChangedNotification notifies clients that the resource list has changed
func (s *Server) SendResourcesListChangedNotification() {
	s.SendNotification("notifications/resources/list_changed", map[string]interface{}{})
	s.log.Debugw("sent resources/list_changed notification")
}

// RegisterGameTool registers a tool for a specific game and tracks it for cleanup
func (s *Server) RegisterGameTool(gameId string, tool Tool, handler func(args map[string]interface{}) (*ToolResult, error), normalizationConfig *config.ToolNormalizationConfig) {
	s.RegisterToolWithConfig(tool, handler, normalizationConfig)

	// Track which game this tool belongs to
	trackedToolName := tool.Name
	if normalizationConfig != nil && normalizationConfig.EnableOpenAINormalization {
		normalizedResult := util.NormalizeToolNameForOpenAI(tool.Name, normalizationConfig.MaxToolNameLength)
		if normalizedResult.WasNormalized {
			trackedToolName = normalizedResult.NormalizedName
		}
	}

	s.mu.Lock()
	for _, existing := range s.gameTools[gameId] {
		if existing == trackedToolName {
			if gabpName := toolMetaString(tool, toolMetaGABPName); gabpName != "" {
				s.registerGameToolAliasesLocked(gameId, gabpName, trackedToolName)
			}
			s.mu.Unlock()
			return
		}
	}
	s.gameTools[gameId] = append(s.gameTools[gameId], trackedToolName)
	if gabpName := toolMetaString(tool, toolMetaGABPName); gabpName != "" {
		s.registerGameToolAliasesLocked(gameId, gabpName, trackedToolName)
	}
	s.mu.Unlock()
}

// RegisterGameResource registers a resource for a specific game and tracks it for cleanup
func (s *Server) RegisterGameResource(gameId string, resource Resource, handler func() ([]Content, error)) {
	s.RegisterResource(resource, handler)

	// Track which game this resource belongs to
	s.mu.Lock()
	for _, existing := range s.gameResources[gameId] {
		if existing == resource.URI {
			s.mu.Unlock()
			return
		}
	}
	s.gameResources[gameId] = append(s.gameResources[gameId], resource.URI)
	s.mu.Unlock()
}

// CleanupGameResources removes all tools and resources for a specific game
func (s *Server) CleanupGameResources(gameId string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	toolsRemoved := 0
	resourcesRemoved := 0

	// Remove game-specific tools
	if toolNames, exists := s.gameTools[gameId]; exists {
		for _, toolName := range toolNames {
			if _, exists := s.tools[toolName]; exists {
				delete(s.tools, toolName)
				toolsRemoved++
			}
		}
		delete(s.gameTools, gameId)
		s.deleteGameToolAliasesLocked(gameId)
	}

	// Remove game-specific resources
	if resourceURIs, exists := s.gameResources[gameId]; exists {
		for _, resourceURI := range resourceURIs {
			if _, exists := s.resources[resourceURI]; exists {
				delete(s.resources, resourceURI)
				resourcesRemoved++
			}
		}
		delete(s.gameResources, gameId)
	}
	s.deleteGameToolAliasesLocked(gameId)

	if toolsRemoved > 0 || resourcesRemoved > 0 {
		s.log.Infow("cleaned up game resources", "gameId", gameId, "toolsRemoved", toolsRemoved, "resourcesRemoved", resourcesRemoved)

		if resourcesRemoved > 0 {
			s.SendResourcesListChangedNotification()
		}
	}
}

// CleanupGABPConnection closes the GABP connection for a game
func (s *Server) CleanupGABPConnection(gameId string) {
	s.mu.Lock()
	// Clean up GABP client connection
	if client, exists := s.gabpClients[gameId]; exists {
		if err := client.Close(); err != nil {
			s.log.Warnw("error closing GABP client", "gameId", gameId, "error", err)
		}
		delete(s.gabpClients, gameId)
		s.log.Debugw("cleaned up GABP client connection", "gameId", gameId)
	}
	// Pop the attachment reference under s.mu, but clear the PERSISTED
	// record only after releasing it — clearBridgeAttachment takes the
	// per-game transition lock, and holding s.mu across it inverts the
	// lock order that status/removal paths use (transition lock, then
	// s.mu via bridgeBound), an ABBA cycle (review round 9).
	ref, hadRef := s.takeBridgeAttachmentRefLocked(gameId)
	s.clearGameAttentionStateLocked(gameId)
	delete(s.gabpDisconnects, gameId)
	s.deleteGameToolAliasesLocked(gameId)
	s.mu.Unlock()

	if hadRef {
		s.clearBridgeAttachment(gameId, ref.launchID, ref.connectionID)
	}
}

// CleanupBridgeConfig removes the bridge configuration file for a game
func (s *Server) CleanupBridgeConfig(gameId string) {
	cp, err := config.NewConfigPaths(s.configDir)
	if err != nil {
		s.log.Warnw("failed to create config paths for cleanup", "gameId", gameId, "error", err)
		return
	}

	bridgePath := cp.GetBridgeConfigPath(gameId)

	if err := os.Remove(bridgePath); err != nil {
		// Don't log as error since file might not exist
		s.log.Debugw("bridge config cleanup", "gameId", gameId, "path", bridgePath, "result", err.Error())
	} else {
		s.log.Debugw("cleaned up bridge config", "gameId", gameId, "path", bridgePath)
	}
}

// Internal cleanup methods that don't acquire locks (for use when mutex is already held)

// cleanupGameResourcesInternal removes game-specific resources without acquiring mutex
func (s *Server) cleanupGameResourcesInternal(gameId string) {
	toolsRemoved := 0
	resourcesRemoved := 0

	// Remove game-specific tools
	if toolNames, exists := s.gameTools[gameId]; exists {
		for _, toolName := range toolNames {
			if _, exists := s.tools[toolName]; exists {
				delete(s.tools, toolName)
				toolsRemoved++
			}
		}
		delete(s.gameTools, gameId)
		s.deleteGameToolAliasesLocked(gameId)
	}

	// Remove game-specific resources
	if resourceURIs, exists := s.gameResources[gameId]; exists {
		for _, resourceURI := range resourceURIs {
			if _, exists := s.resources[resourceURI]; exists {
				delete(s.resources, resourceURI)
				resourcesRemoved++
			}
		}
		delete(s.gameResources, gameId)
	}
	s.deleteGameToolAliasesLocked(gameId)

	if toolsRemoved > 0 || resourcesRemoved > 0 {
		s.log.Infow("cleaned up game resources", "gameId", gameId, "toolsRemoved", toolsRemoved, "resourcesRemoved", resourcesRemoved)

		// Note: We cannot send notifications here because that might require acquiring locks
		// The caller should handle notifications separately if needed
	}
}

// cleanupGABPConnectionInternal cleans up GABP connection without acquiring
// mutex. It returns the popped attachment reference (if any) so the caller
// clears the PERSISTED record AFTER releasing s.mu — clearBridgeAttachment
// takes the transition lock, and clearing it under s.mu would invert the
// lock order (review round 9).
func (s *Server) cleanupGABPConnectionInternal(gameId string) (bridgeAttachmentRef, bool) {
	// Clean up GABP client connection
	if client, exists := s.gabpClients[gameId]; exists {
		if err := client.Close(); err != nil {
			s.log.Warnw("error closing GABP client", "gameId", gameId, "error", err)
		}
		delete(s.gabpClients, gameId)
		s.log.Debugw("cleaned up GABP client connection", "gameId", gameId)
	}
	ref, ok := s.takeBridgeAttachmentRefLocked(gameId)
	s.clearGameAttentionStateLocked(gameId)
	delete(s.gabpDisconnects, gameId)
	return ref, ok
}

// cleanupBridgeConfigInternal removes bridge config without acquiring mutex
func (s *Server) cleanupBridgeConfigInternal(gameId string) {
	cp, err := config.NewConfigPaths(s.configDir)
	if err != nil {
		s.log.Warnw("failed to create config paths for cleanup", "gameId", gameId, "error", err)
		return
	}

	bridgePath := cp.GetBridgeConfigPath(gameId)

	if err := os.Remove(bridgePath); err != nil {
		// Don't log as error since file might not exist
		s.log.Debugw("bridge config cleanup", "gameId", gameId, "path", bridgePath, "result", err.Error())
	} else {
		s.log.Debugw("cleaned up bridge config", "gameId", gameId, "path", bridgePath)
	}
}

func (s *Server) Serve(r io.Reader, w io.Writer) error {
	// MCP stdio uses Content-Length framing. Keep newline-delimited JSON as a
	// fallback so existing local clients keep working.
	reader := util.NewAutoFrameReader(r)
	writer := util.NewAutoFrameWriter(w)
	writerRegistered := false

	// Clean up writer on exit
	defer func() {
		if writerRegistered {
			s.writersMu.Lock()
			// Find and remove writer from slice (safer than using index)
			for i, w := range s.writers {
				if w == writer {
					s.writers = append(s.writers[:i], s.writers[i+1:]...)
					break
				}
			}
			s.writersMu.Unlock()
		}
	}()

	for {
		var msg Message
		if err := reader.ReadJSON(&msg); err != nil {
			if err == io.EOF {
				break
			}
			s.log.Errorw("failed to read message", "error", err)
			continue
		}

		if !writerRegistered {
			writer.SetMode(reader.Mode())
			s.writersMu.Lock()
			s.writers = append(s.writers, writer)
			s.writersMu.Unlock()
			writerRegistered = true
		}

		s.log.Debugw("received message", "method", msg.Method, "id", msg.ID)

		response := s.handleMessage(&msg)
		if response != nil {
			if err := writer.WriteJSON(response); err != nil {
				s.log.Errorw("failed to write response", "error", err)
				return err
			}
		}
	}

	return nil
}

// HandleMessage is a public method for testing tool calls
func (s *Server) HandleMessage(msg *Message) *Message {
	return s.handleMessage(msg)
}

func (s *Server) handleMessage(msg *Message) *Message {
	if msg.ID == nil {
		return s.handleNotification(msg)
	}

	switch msg.Method {
	case "initialize":
		return s.handleInitialize(msg)
	case "tools/list":
		return s.handleToolsList(msg)
	case "tools/call":
		return s.handleToolsCall(msg)
	case "resources/list":
		return s.handleResourcesList(msg)
	case "resources/read":
		return s.handleResourcesRead(msg)
	default:
		return NewError(msg.ID, -32601, "Method not found", nil)
	}
}

func (s *Server) handleNotification(msg *Message) *Message {
	switch msg.Method {
	case "notifications/initialized", "initialized":
		s.log.Debugw("client initialized notification received")
	default:
		// Notifications never receive responses. Ignore unsupported ones so
		// spec-compliant clients can continue after initialize.
		s.log.Debugw("ignoring unsupported notification", "method", msg.Method)
	}

	return nil
}

func (s *Server) handleInitialize(msg *Message) *Message {
	result := InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{
				ListChanged: false,
			},
			Resources: &ResourcesCapability{
				Subscribe:   false,
				ListChanged: true,
			},
		},
		ServerInfo: ServerInfo{
			Name:    "gabs",
			Version: version.Get(),
		},
		Instructions: ServerInstructions,
	}
	return NewResponse(msg.ID, result)
}

func (s *Server) handleToolsList(msg *Message) *Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tools := make([]Tool, 0, len(s.tools))
	gameToolNames := make(map[string]struct{})
	for _, toolNames := range s.gameTools {
		for _, toolName := range toolNames {
			gameToolNames[toolName] = struct{}{}
		}
	}

	for name, handler := range s.tools {
		if _, isGameTool := gameToolNames[name]; isGameTool {
			continue
		}

		tool := handler.Tool
		if s.stripOutputSchema {
			tool.OutputSchema = nil
		}
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})

	result := ToolsListResult{Tools: tools}
	return NewResponse(msg.ID, result)
}

func (s *Server) findToolHandlerLocked(name string) (*ToolHandler, bool) {
	if handler, exists := s.tools[name]; exists {
		return handler, true
	}

	for _, handler := range s.tools {
		for _, alias := range toolNameAliases("", handler.Tool) {
			if alias == name {
				return handler, true
			}
		}
	}

	return nil, false
}

func (s *Server) handleToolsCall(msg *Message) *Message {
	var params ToolCallParams
	paramsBytes, err := json.Marshal(msg.Params)
	if err != nil {
		return NewError(msg.ID, -32602, "Invalid params", err.Error())
	}

	// Numbers must survive the round trip exactly: json.Number marshals as
	// the original literal, and the re-decode preserves it (design/03).
	if err := util.UnmarshalPreservingNumbers(paramsBytes, &params); err != nil {
		return NewError(msg.ID, -32602, "Invalid params", err.Error())
	}

	s.mu.RLock()
	handler, exists := s.findToolHandlerLocked(params.Name)
	s.mu.RUnlock()

	if !exists {
		if result, handled := s.callUnmirroredGABPTool(params.Name, params.Arguments); handled {
			return NewResponse(msg.ID, result)
		}
		return NewError(msg.ID, -32601, "Tool not found", params.Name)
	}

	result, err := handler.Handler(params.Arguments)
	if err != nil {
		return NewError(msg.ID, -32603, "Tool execution failed", err.Error())
	}

	// Central completion: every core-management stable failure carries
	// causeClass + track record + next actions, filled independently (F2).
	s.completeFailureAttribution(params.Name, result)

	return NewResponse(msg.ID, result)
}

func (s *Server) callUnmirroredGABPTool(name string, args map[string]interface{}) (*ToolResult, bool) {
	if args == nil {
		args = map[string]interface{}{}
	}

	gamesConfig := s.gamesConfig
	if gamesConfig == nil {
		return nil, false
	}

	return s.callDirectGABPTool(gamesConfig, "", false, name, args, 30*time.Second)
}

func (s *Server) handleResourcesList(msg *Message) *Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resources := make([]Resource, 0, len(s.resources))
	for _, handler := range s.resources {
		resources = append(resources, handler.Resource)
	}

	result := ResourcesListResult{Resources: resources}
	return NewResponse(msg.ID, result)
}

func (s *Server) handleResourcesRead(msg *Message) *Message {
	var params ResourcesReadParams
	paramsBytes, err := json.Marshal(msg.Params)
	if err != nil {
		return NewError(msg.ID, -32602, "Invalid params", err.Error())
	}

	if err := util.UnmarshalPreservingNumbers(paramsBytes, &params); err != nil {
		return NewError(msg.ID, -32602, "Invalid params", err.Error())
	}

	s.mu.RLock()
	handler, exists := s.resources[params.URI]
	s.mu.RUnlock()

	if !exists {
		return NewError(msg.ID, -32601, "Resource not found", params.URI)
	}

	contents, err := handler.Handler()
	if err != nil {
		return NewError(msg.ID, -32603, "Resource read failed", err.Error())
	}

	result := ResourcesReadResult{Contents: contents}
	return NewResponse(msg.ID, result)
}
