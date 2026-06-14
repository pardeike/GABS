package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/process"
)

const passiveListenerInspectTimeout = time.Second

type bridgeFileDiagnostic struct {
	Present bool
	Path    string
	Port    int
	Token   string
	GameID  string
	Error   string
}

type processEnvDiagnostic struct {
	PID        int
	Present    bool
	Readable   bool
	Port       int
	Token      string
	GameID     string
	BridgePath string
	Error      string
}

type bridgeListenerDiagnostic struct {
	Checked bool
	Open    bool
	Method  string
	Error   string
}

var passiveBridgeListenerStatusFunc = passiveBridgeListenerStatus

func (s *Server) gameStateDiagnostics(game config.GameConfig, status string) map[string]interface{} {
	bridge := s.readBridgeFileDiagnostic(game.ID)
	runtimeState, runtimeErr := process.LoadRuntimeState(game.ID, s.configDir)
	listener := bridgeListenerDiagnostic{}
	if bridge.Present && bridge.Port > 0 {
		listener = passiveBridgeListenerStatusFunc(bridge.Port)
	}

	processEnv := s.inspectGameBridgeEnvironment(game, runtimeState)
	code := "healthy"
	severity := "info"
	message := "No bridge state issue detected."
	warnings := make([]string, 0, 1)

	if runtimeErr != nil {
		code = "runtime-state-error"
		severity = "warning"
		message = fmt.Sprintf("Could not read runtime state: %v", runtimeErr)
	} else if status == "stale-runtime-cleaned" {
		code = "stale-runtime-cleaned"
		severity = "warning"
		message = "Stale runtime and bridge state were removed; start the game again to create fresh bridge state."
	} else if bridge.Error != "" {
		code = "bridge-file-error"
		severity = "warning"
		message = fmt.Sprintf("Could not read bridge.json: %s", bridge.Error)
	} else if bridge.Present && processEnv.Present && bridgeProcessEnvironmentMismatch(bridge, processEnv) {
		if game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId" {
			code = "stale-launcher-environment"
			message = fmt.Sprintf("The running game process has GABP port %d but bridge.json has port %d; the launcher likely reused stale environment.", processEnv.Port, bridge.Port)
		} else {
			code = "bridge-process-env-mismatch"
			message = fmt.Sprintf("The running game process has GABP port %d but bridge.json has port %d.", processEnv.Port, bridge.Port)
		}
		severity = "warning"
	} else if bridge.Present && status == "stopped" && listener.Checked && !listener.Open {
		code = "stale-bridge-file"
		severity = "warning"
		message = "bridge.json exists, but no runtime state or passive listener evidence was found on its port; the file is stale."
	} else if bridge.Present && status == "stopped" && listener.Checked && listener.Open {
		code = "orphan-bridge-listener"
		severity = "warning"
		message = "bridge.json points at an open local port, but GABS has no runtime owner for this game."
	} else if bridge.Present && status == "stopped" {
		code = "unverified-bridge-file"
		severity = "warning"
		message = "bridge.json exists, but GABS has no runtime owner and could not passively verify whether its port is listening."
	} else if runtimeState != nil && !bridge.Present && status != "stopped" && status != "stale-runtime-cleaned" {
		code = "missing-bridge-file"
		severity = "warning"
		message = "runtime.json says the game is running, but bridge.json is missing."
	}

	if (game.LaunchMode == "SteamAppId" || game.LaunchMode == "EpicAppId") && status != "stopped" && status != "stale-runtime-cleaned" && !processEnv.Present {
		warnings = append(warnings, fmt.Sprintf("Could not verify GABP environment on the real game process; %s launchers can reuse stale environment from an already-running launcher.", game.LaunchMode))
	}

	diagnostics := map[string]interface{}{
		"code":               code,
		"severity":           severity,
		"message":            message,
		"bridge":             bridge.structured(),
		"listener":           listener.structured(),
		"processEnvironment": processEnv.structured(),
	}
	if runtimeErr != nil {
		diagnostics["runtime"] = map[string]interface{}{
			"present": false,
			"error":   runtimeErr.Error(),
		}
	} else {
		diagnostics["runtime"] = runtimeStateStructured(runtimeState)
	}
	if len(warnings) > 0 {
		diagnostics["warnings"] = warnings
	}

	return diagnostics
}

func nextActionsForGameStateDiagnostics(game config.GameConfig, diagnostics map[string]interface{}, fallback []map[string]interface{}) []map[string]interface{} {
	if diagnostics == nil {
		return fallback
	}

	code, _ := diagnostics["code"].(string)
	gameArg := map[string]interface{}{"gameId": game.ID}
	switch code {
	case "stale-launcher-environment", "bridge-process-env-mismatch":
		if game.StopProcessName != "" {
			return []map[string]interface{}{
				mcpNextAction("games_stop", gameArg, "Stop the running game so the launcher cannot keep using stale bridge environment."),
				mcpNextAction("games_start", gameArg, "Start the game again to write fresh bridge state and inject matching GABP environment."),
			}
		}
		return []map[string]interface{}{
			mcpNextAction("games_show", gameArg, "Check launch configuration; add stopProcessName or switch to DirectPath/CustomCommand for deterministic recovery."),
			mcpNextAction("games_start", gameArg, "Start only after manually closing the existing game and launcher state."),
		}
	case "orphan-bridge-listener":
		return []map[string]interface{}{
			mcpNextAction("games_connect", gameArg, "Try attaching to the open bridge listener from bridge.json."),
			mcpNextAction("games_status", gameArg, "Refresh status after connect or manual cleanup."),
		}
	case "unverified-bridge-file":
		return []map[string]interface{}{
			mcpNextAction("games_connect", gameArg, "Try the authenticated GABP connection from bridge.json; status does not open a probe connection."),
			mcpNextAction("games_start", gameArg, "If connect fails because the bridge file is stale, start the game again to write fresh bridge/runtime state."),
		}
	case "stale-bridge-file", "missing-bridge-file":
		return []map[string]interface{}{
			mcpNextAction("games_start", gameArg, "Write fresh bridge/runtime state by starting the game through GABS."),
		}
	default:
		return fallback
	}
}

func gameStateDiagnosticMessage(statusItem map[string]interface{}) string {
	diagnostics, _ := statusItem["diagnostics"].(map[string]interface{})
	if diagnostics == nil {
		return ""
	}
	code, _ := diagnostics["code"].(string)
	if code == "" || code == "healthy" {
		return ""
	}
	message, _ := diagnostics["message"].(string)
	return message
}

func (b bridgeFileDiagnostic) structured() map[string]interface{} {
	item := map[string]interface{}{
		"present": b.Present,
		"path":    b.Path,
	}
	if b.Error != "" {
		item["error"] = b.Error
	}
	if b.Present {
		item["port"] = b.Port
		item["gameId"] = b.GameID
		item["tokenFingerprint"] = tokenFingerprint(b.Token)
	}
	return item
}

func (p processEnvDiagnostic) structured() map[string]interface{} {
	item := map[string]interface{}{
		"present":  p.Present,
		"readable": p.Readable,
	}
	if p.PID > 0 {
		item["pid"] = p.PID
	}
	if p.Error != "" {
		item["error"] = p.Error
	}
	if p.Present {
		item["port"] = p.Port
		item["gameId"] = p.GameID
		item["bridgePath"] = p.BridgePath
		item["tokenFingerprint"] = tokenFingerprint(p.Token)
	}
	return item
}

func (l bridgeListenerDiagnostic) structured() map[string]interface{} {
	item := map[string]interface{}{
		"checked": l.Checked,
		"open":    l.Open,
	}
	if l.Method != "" {
		item["method"] = l.Method
	}
	if l.Error != "" {
		item["error"] = l.Error
	}
	return item
}

func runtimeStateStructured(state *process.RuntimeState) map[string]interface{} {
	if state == nil {
		return map[string]interface{}{"present": false}
	}
	item := map[string]interface{}{
		"present":         true,
		"gameId":          state.GameID,
		"status":          state.Status,
		"ownerPid":        state.OwnerPID,
		"ownerInstanceId": state.OwnerInstanceID,
		"gamePid":         state.GamePID,
		"stopProcessName": state.StopProcessName,
		"updatedAt":       state.UpdatedAt.Format(time.RFC3339),
		"resolvedStatus":  process.ResolveRuntimeStateStatus(state),
	}
	return item
}

func (s *Server) readBridgeFileDiagnostic(gameID string) bridgeFileDiagnostic {
	paths, err := config.NewConfigPaths(s.configDir)
	if err != nil {
		return bridgeFileDiagnostic{Error: err.Error()}
	}

	path := paths.GetBridgeConfigPath(gameID)
	diagnostic := bridgeFileDiagnostic{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return diagnostic
		}
		diagnostic.Error = err.Error()
		return diagnostic
	}

	var bridge config.BridgeJSON
	if err := json.Unmarshal(data, &bridge); err != nil {
		diagnostic.Error = err.Error()
		return diagnostic
	}

	diagnostic.Present = true
	diagnostic.Port = bridge.Port
	diagnostic.Token = bridge.Token
	diagnostic.GameID = bridge.GameId
	return diagnostic
}

func (s *Server) inspectGameBridgeEnvironment(game config.GameConfig, runtimeState *process.RuntimeState) processEnvDiagnostic {
	pids := make([]int, 0, 4)
	if runtimeState != nil && runtimeState.GamePID > 0 && process.IsProcessAlive(runtimeState.GamePID) {
		pids = append(pids, runtimeState.GamePID)
	}
	if game.StopProcessName != "" {
		found, err := process.FindProcessesByName(game.StopProcessName)
		if err == nil {
			for _, pid := range found {
				if !containsPID(pids, pid) {
					pids = append(pids, pid)
				}
			}
		}
	}

	var firstError string
	for _, pid := range pids {
		env, readable, err := readProcessBridgeEnvironment(pid)
		if err != nil && firstError == "" {
			firstError = err.Error()
		}
		if !readable {
			continue
		}

		diagnostic := processEnvDiagnostic{
			PID:        pid,
			Readable:   true,
			Port:       parseInt(env["GABP_SERVER_PORT"]),
			Token:      env["GABP_TOKEN"],
			GameID:     env["GABS_GAME_ID"],
			BridgePath: env["GABS_BRIDGE_PATH"],
		}
		diagnostic.Present = diagnostic.Port > 0 || diagnostic.Token != "" || diagnostic.GameID != "" || diagnostic.BridgePath != ""
		if diagnostic.Present {
			return diagnostic
		}
	}

	return processEnvDiagnostic{
		PID:      firstPID(pids),
		Readable: len(pids) > 0 && firstError == "",
		Error:    firstError,
	}
}

func readProcessBridgeEnvironment(pid int) (map[string]string, bool, error) {
	switch runtime.GOOS {
	case "linux":
		return readLinuxProcessEnvironment(pid)
	case "windows":
		return nil, false, fmt.Errorf("process environment inspection is not supported on Windows")
	default:
		return readPsProcessEnvironment(pid)
	}
}

func readLinuxProcessEnvironment(pid int) (map[string]string, bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, false, err
	}

	env := make(map[string]string)
	for _, part := range strings.Split(string(data), "\x00") {
		addBridgeEnvironmentValue(env, part)
	}
	return env, true, nil
}

func readPsProcessEnvironment(pid int) (map[string]string, bool, error) {
	output, err := exec.Command("ps", "eww", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil, false, err
	}

	env := make(map[string]string)
	for _, field := range strings.Fields(string(output)) {
		addBridgeEnvironmentValue(env, field)
	}
	return env, true, nil
}

func addBridgeEnvironmentValue(env map[string]string, value string) {
	for _, key := range []string{"GABP_SERVER_PORT", "GABP_TOKEN", "GABS_GAME_ID", "GABS_BRIDGE_PATH"} {
		prefix := key + "="
		if strings.HasPrefix(value, prefix) {
			env[key] = strings.TrimPrefix(value, prefix)
			return
		}
	}
}

func bridgeProcessEnvironmentMismatch(bridgeFile bridgeFileDiagnostic, processEnv processEnvDiagnostic) bool {
	if !bridgeFile.Present || !processEnv.Present {
		return false
	}
	if processEnv.Port > 0 && bridgeFile.Port > 0 && processEnv.Port != bridgeFile.Port {
		return true
	}
	if processEnv.Token != "" && bridgeFile.Token != "" && processEnv.Token != bridgeFile.Token {
		return true
	}
	if processEnv.GameID != "" && bridgeFile.GameID != "" && processEnv.GameID != bridgeFile.GameID {
		return true
	}
	return false
}

func passiveBridgeListenerStatus(port int) bridgeListenerDiagnostic {
	if port <= 0 {
		return bridgeListenerDiagnostic{}
	}

	var errors []string
	for _, check := range passiveBridgeListenerChecks() {
		checked, open, err := check.run(port)
		if err == nil {
			return bridgeListenerDiagnostic{
				Checked: true,
				Open:    open,
				Method:  check.name,
			}
		}
		if checked {
			return bridgeListenerDiagnostic{
				Checked: true,
				Open:    false,
				Method:  check.name,
				Error:   err.Error(),
			}
		}
		errors = append(errors, fmt.Sprintf("%s: %v", check.name, err))
	}

	return bridgeListenerDiagnostic{
		Error: strings.Join(errors, "; "),
	}
}

type passiveBridgeListenerCheck struct {
	name string
	run  func(port int) (checked bool, open bool, err error)
}

func passiveBridgeListenerChecks() []passiveBridgeListenerCheck {
	switch runtime.GOOS {
	case "linux":
		return []passiveBridgeListenerCheck{
			{name: "ss", run: ssTCPListenStatus},
			{name: "netstat", run: linuxNetstatTCPListenStatus},
			{name: "lsof", run: lsofTCPListenStatus},
		}
	case "windows":
		return []passiveBridgeListenerCheck{
			{name: "netstat", run: windowsNetstatTCPListenStatus},
		}
	default:
		return []passiveBridgeListenerCheck{
			{name: "netstat", run: bsdNetstatTCPListenStatus},
			{name: "lsof", run: lsofTCPListenStatus},
		}
	}
}

func lsofTCPListenStatus(port int) (bool, bool, error) {
	output, err := passiveListenerCommandOutput("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN")
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			if _, ok := err.(*exec.ExitError); ok {
				return true, false, nil
			}
			return false, false, err
		}
		return false, false, fmt.Errorf("%v: %s", err, compactCommandOutput(trimmed))
	}
	return true, trimmed != "", nil
}

func ssTCPListenStatus(port int) (bool, bool, error) {
	output, err := passiveListenerCommandOutput("ss", "-ltnH")
	if err != nil {
		return false, false, commandError(err, output)
	}
	return true, outputHasListeningLocalPort(string(output), port, 3), nil
}

func linuxNetstatTCPListenStatus(port int) (bool, bool, error) {
	output, err := passiveListenerCommandOutput("netstat", "-ltn")
	if err != nil {
		return false, false, commandError(err, output)
	}
	return true, outputHasListeningLocalPort(string(output), port, 3), nil
}

func bsdNetstatTCPListenStatus(port int) (bool, bool, error) {
	output, err := passiveListenerCommandOutput("netstat", "-an", "-p", "tcp")
	if err != nil {
		return false, false, commandError(err, output)
	}
	return true, outputHasListeningLocalPort(string(output), port, 3), nil
}

func windowsNetstatTCPListenStatus(port int) (bool, bool, error) {
	output, err := passiveListenerCommandOutput("netstat", "-an", "-p", "TCP")
	if err != nil {
		return false, false, commandError(err, output)
	}
	return true, outputHasListeningLocalPort(string(output), port, 1), nil
}

func passiveListenerCommandOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), passiveListenerInspectTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return output, fmt.Errorf("%s timed out after %s", name, passiveListenerInspectTimeout)
	}
	return output, err
}

func outputHasListeningLocalPort(output string, port int, localAddressField int) bool {
	for _, line := range strings.Split(output, "\n") {
		upper := strings.ToUpper(line)
		if !strings.Contains(upper, "LISTEN") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) <= localAddressField {
			continue
		}
		if localAddressHasPort(fields[localAddressField], port) {
			return true
		}
	}
	return false
}

func localAddressHasPort(address string, port int) bool {
	portText := strconv.Itoa(port)
	address = strings.Trim(address, "[]")
	return strings.HasSuffix(address, ":"+portText) ||
		strings.HasSuffix(address, "."+portText) ||
		strings.HasSuffix(address, "]:"+portText)
}

func commandError(err error, output []byte) error {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return err
	}
	return fmt.Errorf("%v: %s", err, compactCommandOutput(trimmed))
}

func compactCommandOutput(output string) string {
	output = strings.Join(strings.Fields(output), " ")
	if len(output) > 240 {
		return output[:240] + "..."
	}
	return output
}

func tokenFingerprint(token string) string {
	if strings.TrimSpace(token) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func parseInt(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func containsPID(pids []int, pid int) bool {
	for _, existing := range pids {
		if existing == pid {
			return true
		}
	}
	return false
}

func firstPID(pids []int) int {
	if len(pids) == 0 {
		return 0
	}
	return pids[0]
}
