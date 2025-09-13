package process

import "time"

type LaunchSpec struct {
	GameId     string
	Mode       string // DirectPath|SteamAppId|EpicAppId|CustomCommand
	PathOrId   string
	Args       []string
	WorkingDir string
}

type Controller struct {
	// PROMPT: fields for cmd process, stdout/err capture, platform adapters.
}

func (c *Controller) Configure(spec LaunchSpec) error {
	// PROMPT: validate spec and prepare platform-specific launch.
	return nil
}

func (c *Controller) Start() error {
	// PROMPT: Write bridge.json before launch.
	// PROMPT: For DirectPath: exec + capture stdio.
	// PROMPT: For Steam/Epic: shell out to launcher protocols and rely on logs.
	return nil
}

func (c *Controller) Stop(grace time.Duration) error {
	// PROMPT: Try graceful quit (CloseMainWindow on Windows, SIGTERM on Unix),
	// then hard kill after 'grace'.
	return nil
}

func (c *Controller) Kill() error {
	// PROMPT: Immediate termination platform-safely.
	return nil
}

func (c *Controller) Restart() error {
	// PROMPT: Stop then Start, preserve launchId and bridge.json.
	return nil
}

// PROMPT: Expose MCP tools: bridge.app.start|stop|kill|restart.
// PROMPT: Surface stdout/stderr lines as GABP events via Mirror.
