//go:build !darwin

package steam

func defaultNativeReadinessProbe(uint32) probeObservation {
	return probeObservation{
		State:  probeStateUnavailable,
		Stage:  ReadinessStageClientLibrary,
		Detail: "functional Steam readiness probing is available only on macOS",
	}
}
