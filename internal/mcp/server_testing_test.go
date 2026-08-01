package mcp

import (
	"context"
	"testing"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/process"
	"github.com/pardeike/gabs/internal/util"
)

// NewServerForTesting creates a server with shorter timeouts for testing. Its
// config directory is a caller-owned t.TempDir() — the test framework removes
// it, so no gabs-test-isolated dir is ever leaked (round 13 F6) and it never
// resolves to the real ~/.gabs (round 12 F4). Shutdown is registered via
// t.Cleanup, so EVERY server's background tasks join before teardown (round 13
// F3), whether or not the test calls Shutdown itself.
func NewServerForTesting(tb testing.TB, log util.Logger) *Server {
	tb.Helper()
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	s := &Server{
		log:             log,
		tools:           make(map[string]*ToolHandler),
		resources:       make(map[string]*ResourceHandler),
		games:           make(map[string]process.ControllerInterface),
		configDir:       tb.TempDir(),
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
		shutdownCtx:     shutdownCtx,
		shutdownCancel:  shutdownCancel,
		newController:   func() process.ControllerInterface { return process.NewController() },
	}
	tb.Cleanup(s.Shutdown)
	return s
}
