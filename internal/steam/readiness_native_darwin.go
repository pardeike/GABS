//go:build darwin

package steam

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ebitengine/purego"
)

// Steam exports versioned C accessors for ISteamApps. The read-only app-state
// calls below use Steam's corresponding flat C wrappers, avoiding C++ vtables.
var compatibleSteamAppsAccessors = []string{
	"SteamAPI_SteamApps_v016", "SteamAPI_SteamApps_v015", "SteamAPI_SteamApps_v014",
	"SteamAPI_SteamApps_v013", "SteamAPI_SteamApps_v012", "SteamAPI_SteamApps_v011",
	"SteamAPI_SteamApps_v010", "SteamAPI_SteamApps_v009", "SteamAPI_SteamApps_v008",
}

func defaultNativeReadinessProbe(appID uint32) probeObservation {
	restoreIdentity := setProbeSteamAppIdentity(appID)
	defer restoreIdentity()
	observation := probeSteamClientLibraries(steamClientLibraryCandidates())
	if observation.State != probeStateReady {
		return observation
	}
	return probeSteamAPILibraries(steamAPILibraryCandidates(), appID)
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

func steamAPILibraryCandidates() []string {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		steamRoot := filepath.Join(home, "Library", "Application Support", "Steam", "Steam.AppBundle", "Steam", "Contents")
		candidates = append(candidates,
			filepath.Join(steamRoot, "MacOS", "Frameworks", "Steam Helper.app", "Contents", "MacOS", "libsteam_api.dylib"),
			filepath.Join(steamRoot, "Frameworks", "Steam Helper.app", "Contents", "MacOS", "libsteam_api.dylib"),
		)
	}
	candidates = append(candidates,
		"/Applications/Steam.app/Contents/MacOS/Frameworks/Steam Helper.app/Contents/MacOS/libsteam_api.dylib",
		"/Applications/Steam.app/Contents/Frameworks/Steam Helper.app/Contents/MacOS/libsteam_api.dylib",
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

func appStateObservation(subscribed, installed bool) probeObservation {
	if subscribed && installed {
		return probeObservation{State: probeStateReady, Stage: ReadinessStageAppState}
	}
	detail := "Steam has not loaded the configured app's subscription and install state"
	if installed {
		detail = "Steam has loaded the configured app's install state but not its subscription state"
	} else if subscribed {
		detail = "Steam has loaded the configured app's subscription state but not its install state"
	}
	return probeObservation{State: probeStateNotReady, Stage: ReadinessStageAppState, Detail: detail}
}

func probeSteamAPILibraries(candidates []string, appID uint32) probeObservation {
	var failures []string
	for _, path := range candidates {
		handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		observation := probeLoadedSteamAPI(handle, path, appID)
		if err := purego.Dlclose(handle); err != nil && observation.Detail == "" {
			observation.Detail = fmt.Sprintf("Steam API library cleanup: %v", err)
		}
		return observation
	}
	detail := "Steam API library was not loadable"
	if len(failures) > 0 {
		detail += ": " + strings.Join(failures, "; ")
	}
	return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageSteamAPI, Detail: truncateProbeDetail(detail)}
}

func probeLoadedSteamAPI(handle uintptr, path string, appID uint32) probeObservation {
	initSymbol, err := purego.Dlsym(handle, "SteamAPI_InitFlat")
	if err != nil {
		return missingSteamAPISymbol(path, "SteamAPI_InitFlat", err)
	}
	shutdownSymbol, err := purego.Dlsym(handle, "SteamAPI_Shutdown")
	if err != nil {
		return missingSteamAPISymbol(path, "SteamAPI_Shutdown", err)
	}
	appsAccessorSymbol := uintptr(0)
	for _, symbol := range compatibleSteamAppsAccessors {
		appsAccessorSymbol, _ = purego.Dlsym(handle, symbol)
		if appsAccessorSymbol != 0 {
			break
		}
	}
	if appsAccessorSymbol == 0 {
		return probeObservation{State: probeStateUnavailable, Stage: ReadinessStageAppInterface, Detail: "Steam API library exposes no compatible app-state accessor"}
	}
	isSubscribedSymbol, err := purego.Dlsym(handle, "SteamAPI_ISteamApps_BIsSubscribedApp")
	if err != nil {
		return missingSteamAPIAppSymbol(path, "SteamAPI_ISteamApps_BIsSubscribedApp", err)
	}
	isInstalledSymbol, err := purego.Dlsym(handle, "SteamAPI_ISteamApps_BIsAppInstalled")
	if err != nil {
		return missingSteamAPIAppSymbol(path, "SteamAPI_ISteamApps_BIsAppInstalled", err)
	}
	var initFlat func([]byte) int32
	var shutdown func()
	var appsAccessor func() uintptr
	var isSubscribedApp func(uintptr, uint32) bool
	var isAppInstalled func(uintptr, uint32) bool
	purego.RegisterFunc(&initFlat, initSymbol)
	purego.RegisterFunc(&shutdown, shutdownSymbol)
	purego.RegisterFunc(&appsAccessor, appsAccessorSymbol)
	purego.RegisterFunc(&isSubscribedApp, isSubscribedSymbol)
	purego.RegisterFunc(&isAppInstalled, isInstalledSymbol)
	message := make([]byte, 1024)
	result := initFlat(message)
	detail := strings.TrimSpace(strings.TrimRight(string(message), "\x00"))
	initialization := steamAPIInitObservation(result, detail)
	if initialization.State != probeStateReady {
		return initialization
	}
	defer shutdown()
	apps := appsAccessor()
	if apps == 0 {
		return probeObservation{State: probeStateNotReady, Stage: ReadinessStageAppInterface, Detail: "Steam app-state interface is not ready for the active user"}
	}
	appState := appStateObservation(isSubscribedApp(apps, appID), isAppInstalled(apps, appID))
	if appState.State != probeStateReady {
		return appState
	}
	return probeObservation{State: probeStateReady, Stage: ReadinessStageAppState}
}

func steamAPIInitObservation(result int32, detail string) probeObservation {
	if result == 0 {
		return probeObservation{State: probeStateReady, Stage: ReadinessStageSteamAPI}
	}
	if detail == "" {
		detail = fmt.Sprintf("SteamAPI_InitFlat returned result %d", result)
	}
	return probeObservation{State: probeStateNotReady, Stage: ReadinessStageSteamAPI, Detail: detail}
}

func setProbeSteamAppIdentity(appID uint32) func() {
	value := fmt.Sprintf("%d", appID)
	type priorValue struct {
		name, value string
		present     bool
	}
	prior := make([]priorValue, 0, 2)
	for _, name := range []string{"SteamAppId", "SteamGameId"} {
		old, present := os.LookupEnv(name)
		prior = append(prior, priorValue{name: name, value: old, present: present})
		_ = os.Setenv(name, value)
	}
	return func() {
		for _, item := range prior {
			if item.present {
				_ = os.Setenv(item.name, item.value)
			} else {
				_ = os.Unsetenv(item.name)
			}
		}
	}
}

func missingSteamSymbol(path, symbol string, err error) probeObservation {
	return probeObservation{
		State:  probeStateUnavailable,
		Stage:  ReadinessStageClientLibrary,
		Detail: truncateProbeDetail(fmt.Sprintf("Steam client library %s has no %s: %v", path, symbol, err)),
	}
}

func missingSteamAPISymbol(path, symbol string, err error) probeObservation {
	return probeObservation{
		State:  probeStateUnavailable,
		Stage:  ReadinessStageSteamAPI,
		Detail: truncateProbeDetail(fmt.Sprintf("Steam API library %s has no %s: %v", path, symbol, err)),
	}
}

func missingSteamAPIAppSymbol(path, symbol string, err error) probeObservation {
	return probeObservation{
		State:  probeStateUnavailable,
		Stage:  ReadinessStageAppInterface,
		Detail: truncateProbeDetail(fmt.Sprintf("Steam API library %s has no %s: %v", path, symbol, err)),
	}
}
