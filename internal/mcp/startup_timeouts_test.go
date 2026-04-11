package mcp

import (
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/config"
	"github.com/pardeike/gabs/internal/util"
)

func TestRegisterGameManagementToolsAppliesConfiguredStartupTimeouts(t *testing.T) {
	server := NewServerForTesting(util.NewLogger("error"))
	gamesConfig := &config.GamesConfig{
		Games: make(map[string]config.GameConfig),
		Timeouts: &config.TimeoutsConfig{
			Startup: &config.StartupTimeoutsConfig{
				ProcessStartSeconds: 12,
				GABPConnectSeconds:  95,
			},
		},
	}

	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	processStartTimeout, gabpConnectTimeout := server.starter.GetTimeouts()
	if processStartTimeout != 12*time.Second {
		t.Fatalf("expected process start timeout 12s, got %v", processStartTimeout)
	}
	if gabpConnectTimeout != 95*time.Second {
		t.Fatalf("expected GABP connect timeout 95s, got %v", gabpConnectTimeout)
	}
}

func TestRegisterGameManagementToolsPreservesTestingTimeoutsWithoutExplicitConfig(t *testing.T) {
	server := NewServerForTesting(util.NewLogger("error"))
	gamesConfig := &config.GamesConfig{
		Games: make(map[string]config.GameConfig),
	}

	server.RegisterGameManagementTools(gamesConfig, 100*time.Millisecond, time.Second)

	processStartTimeout, gabpConnectTimeout := server.starter.GetTimeouts()
	if processStartTimeout != 3*time.Second {
		t.Fatalf("expected testing process start timeout 3s, got %v", processStartTimeout)
	}
	if gabpConnectTimeout != 2*time.Second {
		t.Fatalf("expected testing GABP connect timeout 2s, got %v", gabpConnectTimeout)
	}
}
