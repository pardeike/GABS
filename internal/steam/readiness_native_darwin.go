//go:build darwin

package steam

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ebitengine/purego"
)

func defaultNativeReadinessProbe() probeObservation {
	return probeSteamClientLibraries(steamClientLibraryCandidates())
}

func steamClientLibraryCandidates() []string {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, "Library", "Application Support", "Steam", "Steam.AppBundle", "Steam", "Contents", "MacOS", "steamclient.dylib"),
			filepath.Join(home, "Library", "Application Support", "Steam", "Steam.AppBundle", "Steam", "Contents", "Frameworks", "steamclient.dylib"),
		)
	}
	candidates = append(candidates,
		"/Applications/Steam.app/Contents/MacOS/steamclient.dylib",
		"/Applications/Steam.app/Contents/Frameworks/steamclient.dylib",
	)
	seen := map[string]bool{}
	unique := candidates[:0]
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if !seen[candidate] {
			seen[candidate] = true
			unique = append(unique, candidate)
		}
	}
	return unique
}

func probeSteamClientLibraries(candidates []string) probeObservation {
	var failures []string
	for _, path := range candidates {
		handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		observation := probeLoadedSteamClient(handle, path)
		if err := purego.Dlclose(handle); err != nil && observation.Detail == "" {
			observation.Detail = fmt.Sprintf("client library cleanup: %v", err)
		}
		return observation
	}
	detail := "Steam client library was not loadable"
	if len(failures) > 0 {
		detail += ": " + strings.Join(failures, "; ")
	}
	return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageClientLibrary, Detail: truncateProbeDetail(detail)}
}

func probeLoadedSteamClient(handle uintptr, path string) probeObservation {
	createSymbol, err := purego.Dlsym(handle, "Steam_CreateSteamPipe")
	if err != nil {
		return missingSteamSymbol(path, "Steam_CreateSteamPipe", err)
	}
	connectSymbol, err := purego.Dlsym(handle, "Steam_ConnectToGlobalUser")
	if err != nil {
		return missingSteamSymbol(path, "Steam_ConnectToGlobalUser", err)
	}
	releaseUserSymbol, err := purego.Dlsym(handle, "Steam_ReleaseUser")
	if err != nil {
		return missingSteamSymbol(path, "Steam_ReleaseUser", err)
	}
	releasePipeSymbol, err := purego.Dlsym(handle, "Steam_BReleaseSteamPipe")
	if err != nil {
		return missingSteamSymbol(path, "Steam_BReleaseSteamPipe", err)
	}

	var createPipe func() int32
	var connectGlobalUser func(int32) int32
	var releaseUser func(int32, int32)
	var releasePipe func(int32) bool
	purego.RegisterFunc(&createPipe, createSymbol)
	purego.RegisterFunc(&connectGlobalUser, connectSymbol)
	purego.RegisterFunc(&releaseUser, releaseUserSymbol)
	purego.RegisterFunc(&releasePipe, releasePipeSymbol)

	pipe := createPipe()
	if pipe == 0 {
		return probeObservation{State: probeStateNotReady, Stage: ReadinessStageIPCPipe, Detail: "Steam_CreateSteamPipe returned no pipe"}
	}
	user := connectGlobalUser(pipe)
	if user == 0 {
		_ = releasePipe(pipe)
		return probeObservation{State: probeStateNotReady, Stage: ReadinessStageGlobalUser, Detail: "Steam_ConnectToGlobalUser returned no user"}
	}
	releaseUser(pipe, user)
	_ = releasePipe(pipe)
	return probeObservation{State: probeStateReady, Stage: ReadinessStageGlobalUser}
}

func missingSteamSymbol(path, symbol string, err error) probeObservation {
	return probeObservation{
		State:  probeStateUnavailable,
		Stage:  ReadinessStageClientLibrary,
		Detail: truncateProbeDetail(fmt.Sprintf("Steam client library %s has no %s: %v", path, symbol, err)),
	}
}
