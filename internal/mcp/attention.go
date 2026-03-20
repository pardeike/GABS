package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/gabp"
)

const attentionRefreshTimeout = 2 * time.Second

var attentionGateBypassToolSuffixes = []string{
	"rimbridge/get_bridge_status",
	"rimbridge/list_operation_events",
	"rimbridge/list_logs",
	"rimbridge.get_bridge_status",
	"rimbridge.list_operation_events",
	"rimbridge.list_logs",
}

type gameAttentionState struct {
	Supported     bool
	Current       *gabp.AttentionItem
	LastUpdatedAt time.Time
}

func cloneAttentionItem(item *gabp.AttentionItem) *gabp.AttentionItem {
	if item == nil {
		return nil
	}

	return item.Clone()
}

func cloneGameAttentionState(state *gameAttentionState) *gameAttentionState {
	if state == nil {
		return nil
	}

	return &gameAttentionState{
		Supported:     state.Supported,
		Current:       cloneAttentionItem(state.Current),
		LastUpdatedAt: state.LastUpdatedAt,
	}
}

func (s *Server) setGameAttentionSupport(gameID string, supported bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setGameAttentionSupportLocked(gameID, supported)
}

func (s *Server) setGameAttentionSupportLocked(gameID string, supported bool) {
	state := s.gabpAttention[gameID]
	if state == nil {
		state = &gameAttentionState{}
		s.gabpAttention[gameID] = state
	}
	state.Supported = supported
	if !supported {
		state.Current = nil
	}
	state.LastUpdatedAt = time.Now().UTC()
}

func (s *Server) setGameAttentionCurrent(gameID string, attention *gabp.AttentionItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setGameAttentionCurrentLocked(gameID, attention)
}

func (s *Server) setGameAttentionCurrentLocked(gameID string, attention *gabp.AttentionItem) {
	state := s.gabpAttention[gameID]
	if state == nil {
		state = &gameAttentionState{}
		s.gabpAttention[gameID] = state
	}

	state.Supported = true
	state.Current = cloneAttentionItem(attention)
	state.LastUpdatedAt = time.Now().UTC()
}

func (s *Server) clearGameAttentionState(gameID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearGameAttentionStateLocked(gameID)
}

func (s *Server) clearGameAttentionStateLocked(gameID string) {
	delete(s.gabpAttention, gameID)
}

func (s *Server) getGameAttentionState(gameID string) *gameAttentionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneGameAttentionState(s.gabpAttention[gameID])
}

func (s *Server) getCurrentBlockingAttention(gameID string) *gabp.AttentionItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := s.gabpAttention[gameID]
	if state == nil || state.Current == nil {
		return nil
	}

	if state.Current.Blocking && !strings.EqualFold(state.Current.State, "cleared") {
		return cloneAttentionItem(state.Current)
	}

	return nil
}

func (s *Server) setupGABPAttention(gameID string, client *gabp.Client, timeout time.Duration) {
	if client == nil {
		s.clearGameAttentionState(gameID)
		return
	}

	if !client.SupportsAttention() {
		s.setGameAttentionSupport(gameID, false)
		return
	}

	s.setGameAttentionSupport(gameID, true)

	if current, err := client.GetCurrentAttentionWithTimeout(timeout); err != nil {
		s.log.Warnw("failed to query current attention state during setup", "gameId", gameID, "error", err)
	} else {
		s.setGameAttentionCurrent(gameID, current)
	}

	channels := gabp.AttentionChannels(client.GetCapabilities())
	if len(channels) == 0 {
		return
	}

	if err := client.SubscribeEvents(channels, func(channel string, seq int, payload interface{}) {
		s.handleGABPAttentionEvent(gameID, channel, seq, payload)
	}); err != nil {
		s.log.Warnw("failed to subscribe to GABP attention events", "gameId", gameID, "error", err)
	}
}

func (s *Server) handleGABPAttentionEvent(gameID string, channel string, seq int, payload interface{}) {
	attention, err := decodeAttentionPayload(payload)
	if err != nil {
		s.log.Warnw("failed to decode GABP attention payload", "gameId", gameID, "channel", channel, "seq", seq, "error", err)
		return
	}

	switch channel {
	case gabp.AttentionOpenedChannel, gabp.AttentionUpdatedChannel:
		s.setGameAttentionCurrent(gameID, attention)
	case gabp.AttentionClearedChannel:
		s.setGameAttentionCurrent(gameID, nil)
	default:
		return
	}

	s.log.Debugw("updated attention state from GABP event", "gameId", gameID, "channel", channel, "seq", seq)
}

func decodeAttentionPayload(payload interface{}) (*gabp.AttentionItem, error) {
	if payload == nil {
		return nil, nil
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var attention gabp.AttentionItem
	if err := json.Unmarshal(data, &attention); err != nil {
		return nil, err
	}

	return &attention, nil
}

func (s *Server) refreshCurrentAttention(gameID string, client *gabp.Client, timeout time.Duration) (*gabp.AttentionItem, error) {
	if client == nil {
		return nil, fmt.Errorf("GABP client for game '%s' is not available", gameID)
	}

	if !client.SupportsAttention() {
		s.setGameAttentionSupport(gameID, false)
		return nil, nil
	}

	current, err := client.GetCurrentAttentionWithTimeout(timeout)
	if err != nil {
		if strings.Contains(err.Error(), "attention/current") || strings.Contains(err.Error(), "GABP error 32601") {
			s.setGameAttentionSupport(gameID, false)
		}
		return s.getCurrentBlockingAttention(gameID), err
	}

	s.setGameAttentionCurrent(gameID, current)
	return current, nil
}

func (s *Server) enforceAttentionGate(gameID string, exposedToolName string, client *gabp.Client) *ToolResult {
	if client == nil || !client.SupportsAttention() {
		return nil
	}

	current, err := s.refreshCurrentAttention(gameID, client, attentionRefreshTimeout)
	if err != nil {
		s.log.Warnw("failed to refresh current attention state before tool dispatch", "gameId", gameID, "tool", exposedToolName, "error", err)
	}
	if current == nil {
		current = s.getCurrentBlockingAttention(gameID)
	}
	if current == nil || !current.Blocking || strings.EqualFold(current.State, "cleared") {
		return nil
	}

	return buildAttentionBlockedToolResult(gameID, exposedToolName, current)
}

func shouldBypassAttentionGate(toolNames ...string) bool {
	for _, toolName := range toolNames {
		normalized := strings.TrimSpace(strings.ToLower(toolName))
		if normalized == "" {
			continue
		}

		for _, suffix := range attentionGateBypassToolSuffixes {
			if normalized == suffix || strings.HasSuffix(normalized, "."+suffix) {
				return true
			}
		}
	}

	return false
}

func buildAttentionBlockedToolResult(gameID string, toolName string, attention *gabp.AttentionItem) *ToolResult {
	summary := ""
	if attention != nil && strings.TrimSpace(attention.Summary) != "" {
		summary = fmt.Sprintf(" Summary: %s.", attention.Summary)
	}

	return &ToolResult{
		Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("Tool call '%s' for game '%s' was not executed because important game information requires acknowledgement.%s Review it with games.get_attention, acknowledge it with games.ack_attention, then retry the original call.", toolName, gameID, summary),
		}},
		StructuredContent: map[string]interface{}{
			"executed":  false,
			"status":    "blocked_by_attention",
			"gameId":    gameID,
			"tool":      toolName,
			"attention": attention,
		},
		IsError: true,
	}
}

func (s *Server) resolveAttentionClient(gamesConfig *config.GamesConfig, requestedGame string, hasGameID bool) (*config.GameConfig, *gabp.Client, *ToolResult) {
	if hasGameID {
		game, exists := s.resolveGameId(gamesConfig, requestedGame)
		if !exists {
			return nil, nil, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' not found. Use games.list to see available games.", requestedGame)}},
				IsError: true,
			}
		}

		s.mu.RLock()
		client, connected := s.gabpClients[game.ID]
		s.mu.RUnlock()
		if !connected || !client.IsConnected() {
			disconnectNote := s.describeLastGABPDisconnect(game.ID)
			if disconnectNote != "" {
				disconnectNote = " " + disconnectNote
			}
			return nil, nil, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' is not connected via GABP. Use games.status to verify whether it is still running, then use games.connect or games.start as appropriate.%s", game.ID, disconnectNote)}},
				IsError: true,
			}
		}

		return game, client, nil
	}

	s.mu.RLock()
	connectedGameIDs := make([]string, 0, len(s.gabpClients))
	for gameID, client := range s.gabpClients {
		if client != nil && client.IsConnected() {
			connectedGameIDs = append(connectedGameIDs, gameID)
		}
	}
	s.mu.RUnlock()

	sort.Strings(connectedGameIDs)
	switch len(connectedGameIDs) {
	case 0:
		return nil, nil, &ToolResult{
			Content: []Content{{Type: "text", Text: "No games are currently connected via GABP. Use games.status, games.start, or games.connect first."}},
			IsError: true,
		}
	case 1:
		gameID := connectedGameIDs[0]
		game, exists := gamesConfig.GetGame(gameID)
		if !exists {
			return nil, nil, &ToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("Game '%s' is connected via GABP but is not present in the loaded game configuration.", gameID)}},
				IsError: true,
			}
		}

		s.mu.RLock()
		client := s.gabpClients[gameID]
		s.mu.RUnlock()
		return game, client, nil
	default:
		return nil, nil, &ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Multiple games are connected via GABP (%s). Include gameId to select one.", strings.Join(connectedGameIDs, ", "))}},
			IsError: true,
		}
	}
}
