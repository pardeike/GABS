package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pardeike/gabs/internal/gabp"
	"github.com/pardeike/gabs/internal/process"
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
	// authenticateOnly connects and handshakes but neither publishes an
	// attachment nor mirrors tools — the legacy migration validation mode:
	// the connect handler persists the validated endpoint through the
	// minted launch fence, publishes, and only then exposes tools.
	authenticateOnly bool
}

// NewLegacyMigrationConnector authenticates a captured legacy candidate
// without publication or mirroring (design/07).
func NewLegacyMigrationConnector(server *Server, backoffMin, backoffMax time.Duration) *ServerGABPConnector {
	c := newServerGABPConnector(server, backoffMin, backoffMax, true, 0)
	c.authenticateOnly = true
	return c
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

// staleBridgeCredentialError is the typed connection refusal for
// credentials that are not the current claim's per-launch credential
// (design/03, design/10: stable code stale_bridge_credential).
type staleBridgeCredentialError struct {
	gameID string
}

func (e *staleBridgeCredentialError) Error() string {
	return fmt.Sprintf("stale bridge credential for '%s': the connection credential is not the current launch's per-launch endpoint; the running bridge belongs to an earlier launch environment", e.gameID)
}

// supersededConnectionError is the typed refusal for a connection whose
// attachment could not be published — the claim vanished or was replaced
// during the handshake, or the connection stopped being current. Such a
// client is closed and never mirrors tools (review round 8).
type supersededConnectionError struct {
	gameID string
}

func (e *supersededConnectionError) Error() string {
	return fmt.Sprintf("the launch of '%s' was superseded while the bridge connected; the connection was closed — re-check games_status", e.gameID)
}

// AttemptConnection implements the GABPConnector interface. Connections are
// credential-bound: only the current claim's per-launch endpoint (or the
// legacy migration candidate on the first touch) may authenticate, and a
// client whose credential stops matching before publication is closed —
// it must never survive as GABP evidence or serve mirrored tools.
func (c *ServerGABPConnector) AttemptConnection(ctx context.Context, gameID string, port int, token string) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	c.log.Debugw("attempting GABP connection for game", "gameId", gameID, "addr", addr)

	// Pre-connect credential check: never even dial with a credential that
	// contradicts the current claim's endpoint.
	if claim, err := process.LoadRuntimeState(gameID, c.server.configDir); err == nil && claim != nil &&
		claim.SchemaVersion >= process.RuntimeSchemaVersion && claim.Endpoint != nil &&
		(claim.Endpoint.Port != port || claim.Endpoint.Token != token) {
		return &staleBridgeCredentialError{gameID: gameID}
	}

	// Create GABP client
	client := gabp.NewClient(c.log)
	client.SetDisconnectHandler(func(err error) {
		c.server.dispatchGABPDisconnect(gameID, client, err)
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

	if c.authenticateOnly {
		// Legacy migration validation: the handshake proved the candidate;
		// publication and mirroring are the handler's job, in that order.
		return nil
	}

	// Persist the attachment record (design/04) BEFORE mirroring: an
	// ordinary connection may expose tools only after a successful
	// attachment commit — a client without a persisted launch/connection
	// binding must not survive (review round 8). The record binds to the
	// claim whose endpoint credential authenticated, and only while this
	// client is still the game's current live connection.
	ref, rerr := c.server.recordBridgeAttachment(gameID, client, port, token, func() bool {
		c.server.mu.RLock()
		current := c.server.gabpClients[gameID]
		c.server.mu.RUnlock()
		return current == client && client.IsConnected()
	})
	if rerr != nil {
		c.server.mu.Lock()
		if current, exists := c.server.gabpClients[gameID]; exists && current == client {
			delete(c.server.gabpClients, gameID)
		}
		c.server.mu.Unlock()
		_ = client.Close()
		if errors.Is(rerr, errStaleAttachmentCredential) {
			return &staleBridgeCredentialError{gameID: gameID}
		}
		return &supersededConnectionError{gameID: gameID}
	}

	// The welcome-time delivery report is evaluated against the
	// spawn-pinned digests and persisted under EXACTLY the connection
	// that produced it (the publication result above) — never a
	// reacquired reference (review round 9). Taking the report also
	// discards the raw values from the client.
	c.server.recordContextDelivery(gameID, ref, client.TakeObservedContext())

	if !c.mirrorSynchronously {
		c.startAsyncToolMirroring(gameID, client, ref)
		return nil
	}

	if err := c.setupToolMirroring(ctx, gameID, client); err != nil {
		// A terminal setup failure closes and removes the exact client —
		// it must not linger connected without mirrored state (round 9).
		c.server.HandleUnexpectedGABPDisconnect(gameID, client, err)
		c.server.mu.Lock()
		if current, exists := c.server.gabpClients[gameID]; exists && current == client {
			delete(c.server.gabpClients, gameID)
		}
		c.server.mu.Unlock()
		_ = client.Close()
		return err
	}

	return nil
}

// MirrorConnectedClient exposes an already-published client's tools — the
// migration flow's final step, after endpoint persist and attachment
// publication both landed.
func (c *ServerGABPConnector) MirrorConnectedClient(ctx context.Context, gameID string, client *gabp.Client) error {
	return c.setupToolMirroring(ctx, gameID, client)
}

func (c *ServerGABPConnector) startAsyncToolMirroring(gameID string, client *gabp.Client, ref bridgeAttachmentRef) {
	c.server.bgWG.Add(1)
	go func() {
		defer c.server.bgWG.Done()
		if c.asyncMirrorDelay > 0 {
			// A shutdown during the pre-mirror delay abandons the mirroring
			// so the goroutine joins promptly (round 12 F4).
			select {
			case <-time.After(c.asyncMirrorDelay):
			case <-c.server.shutdownCh:
				return
			}
		}
		// Revalidate the exact binding before committing any mirroring: a
		// delayed discovery for connection A must never overwrite tools
		// mirrored from a newer connection B (review round 9).
		if !c.server.bridgeBound(gameID)(ref.launchID, ref.connectionID) {
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
	c.server.bgWG.Add(1)
	go func() {
		defer c.server.bgWG.Done()
		c.server.setupGABPAttention(gameID, client, attentionTimeout)
	}()

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
