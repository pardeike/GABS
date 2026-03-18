package mcp

import (
	"context"
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
func (c *ServerGABPConnector) AttemptConnection(ctx context.Context, gameID string, port int, token string) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	c.log.Debugw("attempting GABP connection for game", "gameId", gameID, "addr", addr)

	// Create GABP client
	client := gabp.NewClient(c.log)
	client.SetDisconnectHandler(func(err error) {
		c.server.HandleUnexpectedGABPDisconnect(gameID, client, err)
	})

	// Store client reference for cleanup
	c.server.mu.Lock()
	c.server.gabpClients[gameID] = client
	delete(c.server.gabpDisconnects, gameID)
	c.server.mu.Unlock()

	// Attempt connection with retry logic
	backoffMin := 100 * time.Millisecond
	backoffMax := 2 * time.Second

	err := client.Connect(ctx, addr, token, backoffMin, backoffMax)
	if err != nil {
		c.log.Debugw("GABP connection failed", "gameId", gameID, "addr", addr, "error", err)

		// Clean up client reference on failure
		c.server.mu.Lock()
		if current, exists := c.server.gabpClients[gameID]; exists && current == client {
			delete(c.server.gabpClients, gameID)
		}
		c.server.mu.Unlock()
		return err
	}

	c.log.Infow("GABP connection established", "gameId", gameID, "addr", addr)

	// Mirror synchronously so tools are registered before games.start returns
	if err := c.setupToolMirroring(gameID, client); err != nil {
		c.server.HandleUnexpectedGABPDisconnect(gameID, client, err)
		return err
	}

	return nil
}

// setupToolMirroring syncs GABP tools/resources to the MCP server
func (c *ServerGABPConnector) setupToolMirroring(gameID string, client *gabp.Client) error {
	c.log.Debugw("setting up tool mirroring for game", "gameId", gameID)

	// Sync tools from GABP to MCP
	if err := c.server.syncGABPTools(client, gameID); err != nil {
		c.log.Warnw("failed to sync GABP tools", "gameId", gameID, "error", err)
		return err
	}
	c.log.Infow("GABP tools synchronized successfully", "gameId", gameID)

	// Expose GABP resources as MCP resources
	if err := c.server.exposeGABPResources(client, gameID); err != nil {
		c.log.Warnw("failed to expose GABP resources", "gameId", gameID, "error", err)
		return err
	}
	c.log.Infow("GABP resources exposed successfully", "gameId", gameID)

	return nil
}
