package mcp

import (
	"fmt"
	"time"

	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/util"
)

// ServerGABPConnector implements the GABPConnector interface for the MCP server
type ServerGABPConnector struct {
	server *Server
	log    util.Logger
}

// NewServerGABPConnector creates a new GABP connector for the server
func NewServerGABPConnector(server *Server) *ServerGABPConnector {
	return &ServerGABPConnector{
		server: server,
		log:    server.log,
	}
}

// AttemptConnection implements the GABPConnector interface
func (c *ServerGABPConnector) AttemptConnection(gameID string, port int, token string) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	c.log.Debugw("attempting GABP connection for game", "gameId", gameID, "addr", addr)

	// Create GABP client
	client := gabp.NewClient(c.log)

	// Store client reference for cleanup
	c.server.mu.Lock()
	c.server.gabpClients[gameID] = client
	c.server.mu.Unlock()

	// Attempt connection with retry logic
	backoffMin := 100 * time.Millisecond
	backoffMax := 2 * time.Second
	
	err := client.Connect(addr, token, backoffMin, backoffMax)
	if err != nil {
		c.log.Debugw("GABP connection failed", "gameId", gameID, "addr", addr, "error", err)
		
		// Clean up client reference on failure
		c.server.mu.Lock()
		delete(c.server.gabpClients, gameID)
		c.server.mu.Unlock()
		return false
	}

	c.log.Infow("GABP connection established", "gameId", gameID, "addr", addr)

	// Set up mirroring for dynamic tool discovery
	go c.setupToolMirroring(gameID, client)

	return true
}

// setupToolMirroring sets up the mirroring system for dynamic tool discovery
func (c *ServerGABPConnector) setupToolMirroring(gameID string, client *gabp.Client) {
	// This implements the mirroring logic that was previously in establishGABPConnection
	// We can expand this as needed for the full mirroring system
	c.log.Debugw("setting up tool mirroring for game", "gameId", gameID)
	
	// TODO: Implement full mirroring system
	// For now, we just log that the connection is ready
}