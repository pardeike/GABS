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
	server              *Server
	log                 util.Logger
	backoffMin          time.Duration
	backoffMax          time.Duration
	mirrorSynchronously bool
	asyncMirrorDelay    time.Duration
}

// NewServerGABPConnector creates a new GABP connector for the server
func NewServerGABPConnector(server *Server, backoffMin, backoffMax time.Duration) *ServerGABPConnector {
	return newServerGABPConnector(server, backoffMin, backoffMax, true, 0)
}

func NewAsyncServerGABPConnector(server *Server, backoffMin, backoffMax time.Duration) *ServerGABPConnector {
	return newServerGABPConnector(server, backoffMin, backoffMax, false, 0)
}

func newServerGABPConnector(server *Server, backoffMin, backoffMax time.Duration, mirrorSynchronously bool, asyncMirrorDelay time.Duration) *ServerGABPConnector {
	if backoffMin <= 0 {
		backoffMin = 100 * time.Millisecond
	}
	if backoffMax <= 0 {
		backoffMax = 2 * time.Second
	}

	if asyncMirrorDelay < 0 {
		asyncMirrorDelay = 0
	}

	return &ServerGABPConnector{
		server:              server,
		log:                 server.log,
		backoffMin:          backoffMin,
		backoffMax:          backoffMax,
		mirrorSynchronously: mirrorSynchronously,
		asyncMirrorDelay:    asyncMirrorDelay,
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

	err := client.Connect(ctx, addr, token, c.backoffMin, c.backoffMax)
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

	if !c.mirrorSynchronously {
		c.startAsyncToolMirroring(gameID, client)
		return nil
	}

	if err := c.setupToolMirroring(ctx, gameID, client); err != nil {
		c.server.HandleUnexpectedGABPDisconnect(gameID, client, err)
		return err
	}

	return nil
}

func (c *ServerGABPConnector) startAsyncToolMirroring(gameID string, client *gabp.Client) {
	go func() {
		if c.asyncMirrorDelay > 0 {
			time.Sleep(c.asyncMirrorDelay)
		}
		if !client.IsConnected() {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := c.setupToolMirroring(ctx, gameID, client); err != nil {
			c.log.Warnw("asynchronous GABP tool mirroring failed", "gameId", gameID, "error", err)
		}
	}()
}

// setupToolMirroring syncs GABP tools/resources to the MCP server
func (c *ServerGABPConnector) setupToolMirroring(ctx context.Context, gameID string, client *gabp.Client) error {
	c.log.Debugw("setting up tool mirroring for game", "gameId", gameID)

	// Sync tools from GABP to MCP
	if err := c.server.syncGABPToolsWithTimeout(client, gameID, timeoutFromContextOrDefault(ctx, 30*time.Second)); err != nil {
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

	attentionTimeout := timeoutFromContextOrDefault(ctx, attentionRefreshTimeout)
	if attentionTimeout > attentionRefreshTimeout {
		attentionTimeout = attentionRefreshTimeout
	}
	go c.server.setupGABPAttention(gameID, client, attentionTimeout)

	return nil
}

func timeoutFromContextOrDefault(ctx context.Context, fallback time.Duration) time.Duration {
	if ctx == nil {
		return fallback
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		return fallback
	}

	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Millisecond
	}

	return remaining
}
