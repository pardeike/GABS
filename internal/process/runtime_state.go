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
	GamePID         int       `json:"gamePid,omitempty"`
	StopProcessName string    `json:"stopProcessName,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// NewRuntimeState creates a shared runtime record for the given launch spec.
func NewRuntimeState(spec LaunchSpec, status string) RuntimeState {
	return RuntimeState{
		GameID:          spec.GameId,
		Status:          status,
		OwnerPID:        os.Getpid(),
		StopProcessName: spec.StopProcessName,
		UpdatedAt:       time.Now().UTC(),
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
		pids, err := findProcessesByName(state.StopProcessName)
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

func marshalRuntimeState(state RuntimeState) ([]byte, error) {
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal runtime state: %w", err)
	}
	return data, nil
}
