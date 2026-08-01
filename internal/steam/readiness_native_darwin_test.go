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

func TestSteamAPIMissingLibraryIsUnavailable(t *testing.T) {
	observation := probeSteamAPILibraries([]string{filepath.Join(t.TempDir(), "missing-libsteam-api.dylib")}, 123456)
	if observation.State != probeStateUnavailable || observation.Stage != ReadinessStageSteamAPI {
		t.Fatalf("observation = %+v", observation)
	}
}

func TestSteamAPILibraryCandidatesIncludePerUserBundle(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "Application Support", "Steam", "Steam.AppBundle", "Steam", "Contents", "MacOS", "Frameworks", "Steam Helper.app", "Contents", "MacOS", "libsteam_api.dylib")
	for _, candidate := range steamAPILibraryCandidates() {
		if candidate == want {
			return
		}
	}
	t.Fatalf("per-user Steam API library candidate %q is missing: %v", want, steamAPILibraryCandidates())
}

func TestAppStateRequiresSubscriptionAndInstallation(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		subscribed, installed bool
		want                  probeState
	}{
		{name: "neither loaded", want: probeStateNotReady},
		{name: "install loaded first", installed: true, want: probeStateNotReady},
		{name: "subscription loaded first", subscribed: true, want: probeStateNotReady},
		{name: "both loaded", subscribed: true, installed: true, want: probeStateReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation := appStateObservation(tc.subscribed, tc.installed)
			if observation.State != tc.want || observation.Stage != ReadinessStageAppState {
				t.Fatalf("observation = %+v, want state %q at app_state", observation, tc.want)
			}
		})
	}
}

func TestSteamAPIInitResultMustSucceed(t *testing.T) {
	failed := steamAPIInitObservation(2, "Cannot create IPC pipe")
	if failed.State != probeStateNotReady || failed.Stage != ReadinessStageSteamAPI || failed.Detail == "" {
		t.Fatalf("failed observation = %+v", failed)
	}
	ready := steamAPIInitObservation(0, "")
	if ready.State != probeStateReady || ready.Stage != ReadinessStageSteamAPI || ready.Detail != "" {
		t.Fatalf("ready observation = %+v", ready)
	}
}

func TestProbeSteamAppIdentityIsExplicitAndRestored(t *testing.T) {
	t.Setenv("SteamAppId", "old")
	if err := os.Unsetenv("SteamGameId"); err != nil {
		t.Fatal(err)
	}
	restore := setProbeSteamAppIdentity(123456)
	if os.Getenv("SteamAppId") != "123456" || os.Getenv("SteamGameId") != "123456" {
		t.Fatalf("probe identity was not set: SteamAppId=%q SteamGameId=%q", os.Getenv("SteamAppId"), os.Getenv("SteamGameId"))
	}
	restore()
	if os.Getenv("SteamAppId") != "old" {
		t.Fatalf("SteamAppId restored to %q", os.Getenv("SteamAppId"))
	}
	if _, present := os.LookupEnv("SteamGameId"); present {
		t.Fatalf("SteamGameId remained set to %q", os.Getenv("SteamGameId"))
	}
}
