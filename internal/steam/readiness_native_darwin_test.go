//go:build darwin

package steam

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSteamClientLibraryCandidatesIncludePerUserBundle(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "Application Support", "Steam", "Steam.AppBundle", "Steam", "Contents", "MacOS", "steamclient.dylib")
	for _, candidate := range steamClientLibraryCandidates() {
		if candidate == want {
			return
		}
	}
	t.Fatalf("per-user Steam client library candidate %q is missing: %v", want, steamClientLibraryCandidates())
}

func TestNativeProbeMissingLibraryIsUnavailable(t *testing.T) {
	observation := probeSteamClientLibraries([]string{filepath.Join(t.TempDir(), "missing-steamclient.dylib")})
	if observation.State != probeStateUnavailable || observation.Stage != ReadinessStageClientLibrary {
		t.Fatalf("observation = %+v", observation)
	}
}
