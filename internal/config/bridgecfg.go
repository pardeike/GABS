package config

type BridgeJSON struct {
	Port    int    `json:"port"`
	Token   string `json:"token"`
	GameId  string `json:"gameId"`
	Agent   string `json:"agentName"`
	// PROMPT: Optional extra fields for mod consumption.
}

// PROMPT: Compute per-OS config dir:
// - Windows: %APPDATA%\\GAB\\<gameId>\\bridge.json
// - macOS: ~/Library/Application Support/GAB/<gameId>/bridge.json
// - Linux: $XDG_STATE_HOME/gab/<gameId>/bridge.json or ~/.local/state/gab/<gameId>/bridge.json
// PROMPT: Atomic write (temp + rename). Return (port, token, path).
