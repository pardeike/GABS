package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/pardeike/gabs/internal/config"
)

const (
	RuntimeStateStatusStarting = "starting"
	RuntimeStateStatusRunning  = "running"
)

var ErrRuntimeStateExists = errors.New("runtime state already exists")

// RuntimeState captures the shared on-disk lifecycle for one game so multiple
// GABS processes can avoid racing the same launch.
type RuntimeState struct {
	GameID          string    `json:"gameId"`
	Status          string    `json:"status"`
	OwnerPID        int       `json:"ownerPid"`
	OwnerInstanceID string    `json:"ownerInstanceId,omitempty"`
	OwnerLeaseUntil time.Time `json:"ownerLeaseUntil,omitempty"`
	OwnerLastActive time.Time `json:"ownerLastActive,omitempty"`
	GamePID         int       `json:"gamePid,omitempty"`
	StopProcessName string    `json:"stopProcessName,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// NewRuntimeState creates a shared runtime record for the given launch spec.
func NewRuntimeState(spec LaunchSpec, status string) RuntimeState {
	now := time.Now().UTC()
	return RuntimeState{
		GameID:          spec.GameId,
		Status:          status,
		OwnerPID:        os.Getpid(),
		StopProcessName: spec.StopProcessName,
		OwnerLastActive: now,
		UpdatedAt:       now,
	}
}

// ClaimRuntimeState creates the shared runtime state file if it does not yet exist.
func ClaimRuntimeState(gameID, configDir string, state RuntimeState) error {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return fmt.Errorf("failed to create config paths: %w", err)
	}
	if err := cp.EnsureGameDir(gameID); err != nil {
		return fmt.Errorf("failed to create game config dir: %w", err)
	}

	path := cp.GetRuntimeStatePath(gameID)
	data, err := marshalRuntimeState(state)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrRuntimeStateExists
		}
		return fmt.Errorf("failed to create runtime state: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("failed to write runtime state: %w", err)
	}

	return nil
}

// SaveRuntimeState overwrites the shared runtime state file in-place.
func SaveRuntimeState(gameID, configDir string, state RuntimeState) error {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return fmt.Errorf("failed to create config paths: %w", err)
	}
	if err := cp.EnsureGameDir(gameID); err != nil {
		return fmt.Errorf("failed to create game config dir: %w", err)
	}

	path := cp.GetRuntimeStatePath(gameID)
	data, err := marshalRuntimeState(state)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write runtime state: %w", err)
	}

	return nil
}

// LoadRuntimeState reads the shared runtime state file if present.
func LoadRuntimeState(gameID, configDir string) (*RuntimeState, error) {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create config paths: %w", err)
	}

	path := cp.GetRuntimeStatePath(gameID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read runtime state: %w", err)
	}

	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse runtime state: %w", err)
	}

	return &state, nil
}

// RemoveRuntimeState removes the shared runtime state file for a game.
func RemoveRuntimeState(gameID, configDir string) error {
	cp, err := config.NewConfigPaths(configDir)
	if err != nil {
		return fmt.Errorf("failed to create config paths: %w", err)
	}

	path := cp.GetRuntimeStatePath(gameID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove runtime state: %w", err)
	}

	return nil
}

// ResolveRuntimeStateStatus returns the currently observable status for a runtime state.
func ResolveRuntimeStateStatus(state *RuntimeState) string {
	if state == nil {
		return ""
	}

	if state.GamePID > 0 && isProcessAlive(state.GamePID) {
		if state.Status == RuntimeStateStatusStarting {
			return RuntimeStateStatusStarting
		}
		return RuntimeStateStatusRunning
	}

	if state.StopProcessName != "" {
		pids, err := findProcessesByNameFunc(state.StopProcessName)
		if err == nil && len(pids) > 0 {
			return RuntimeStateStatusRunning
		}
	}

	if state.Status == RuntimeStateStatusStarting && state.OwnerPID > 0 && isProcessAlive(state.OwnerPID) {
		return RuntimeStateStatusStarting
	}

	return ""
}

// RuntimeStateOwnedByAnotherLiveOwner reports whether a different live GABS
// owner still holds the shared runtime state.
func RuntimeStateOwnedByAnotherLiveOwner(state *RuntimeState, currentPID int, currentInstanceID string) bool {
	if state == nil || state.OwnerPID <= 0 {
		return false
	}
	if state.OwnerPID == currentPID && (state.OwnerInstanceID == "" || state.OwnerInstanceID == currentInstanceID) {
		return false
	}

	return isProcessAlive(state.OwnerPID)
}

// RuntimeStateOwnedByAnotherActiveOwner reports whether another live GABS
// owner still holds an unexpired runtime lease.
func RuntimeStateOwnedByAnotherActiveOwner(state *RuntimeState, currentPID int, currentInstanceID string, leaseDuration time.Duration, now time.Time) bool {
	if !RuntimeStateOwnedByAnotherLiveOwner(state, currentPID, currentInstanceID) {
		return false
	}
	return RuntimeOwnerLeaseActive(state, leaseDuration, now)
}

// RefreshRuntimeOwnerLease updates the runtime owner and extends its activity lease.
func RefreshRuntimeOwnerLease(state RuntimeState, ownerPID int, ownerInstanceID string, leaseDuration time.Duration, now time.Time) RuntimeState {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	state.OwnerPID = ownerPID
	state.OwnerInstanceID = ownerInstanceID
	state.OwnerLastActive = now
	if leaseDuration > 0 {
		state.OwnerLeaseUntil = now.Add(leaseDuration)
	} else {
		state.OwnerLeaseUntil = time.Time{}
	}
	return state
}

// RuntimeOwnerLeaseActive reports whether the current owner lease should still
// be treated as active. Older runtime.json files without explicit lease fields
// fall back to UpdatedAt plus the configured lease duration.
func RuntimeOwnerLeaseActive(state *RuntimeState, leaseDuration time.Duration, now time.Time) bool {
	if state == nil {
		return false
	}
	expiresAt := RuntimeOwnerLeaseExpiresAt(state, leaseDuration)
	if expiresAt.IsZero() {
		return true
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	return now.Before(expiresAt)
}

// RuntimeOwnerLeaseExpiresAt resolves the effective lease expiration time for
// both current and pre-lease runtime.json files.
func RuntimeOwnerLeaseExpiresAt(state *RuntimeState, leaseDuration time.Duration) time.Time {
	if state == nil {
		return time.Time{}
	}
	if !state.OwnerLeaseUntil.IsZero() {
		return state.OwnerLeaseUntil
	}
	if leaseDuration <= 0 {
		return time.Time{}
	}
	base := state.OwnerLastActive
	if base.IsZero() {
		base = state.UpdatedAt
	}
	if base.IsZero() {
		return time.Time{}
	}
	return base.Add(leaseDuration)
}

func marshalRuntimeState(state RuntimeState) ([]byte, error) {
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal runtime state: %w", err)
	}
	return data, nil
}
