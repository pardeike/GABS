package process

import (
	"testing"

	"github.com/pardeike/gabs/internal/steam"
)

// Controller.Start must NOT invoke Steam-client assistance (M2.15): the ensure
// step moved to the Stage-2 start manager, so a spawn can never turn assistance
// failure into spawn_failed nor run it twice.
func TestControllerStartDoesNotEnsureSteamClient(t *testing.T) {
	startCalled := false
	restoreClient := steam.SetClientControlForTesting(
		func() (string, []string, error) { startCalled = true; return "/bin/true", nil, nil },
		func() bool { return false }, // client not observable
		0, 0,
	)
	defer restoreClient()
	restoreResolve := SetSteamResolveAppForTesting(func(appID string) (SteamApp, error) {
		return SteamApp{Executable: "/bin/echo"}, nil
	})
	defer restoreResolve()

	c := NewController()
	if err := c.Configure(LaunchSpec{GameId: "g", Mode: "SteamManaged", PathOrId: "123"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("SteamManaged Start must succeed without ensuring the client: %v", err)
	}
	if startCalled {
		t.Fatal("Controller.Start must not invoke Steam-client assistance")
	}
}
