package process

// IsProcessAlive reports whether a PID currently exists.
func IsProcessAlive(pid int) bool {
	return isProcessAlive(pid)
}

// FindProcessesByName returns PIDs whose executable name matches name.
func FindProcessesByName(name string) ([]int, error) {
	return findProcessesByNameFunc(name)
}

// SetFindProcessesByNameForTesting overrides process-name lookup in tests.
func SetFindProcessesByNameForTesting(fn func(string) ([]int, error)) func() {
	previous := findProcessesByNameFunc
	if fn != nil {
		findProcessesByNameFunc = fn
	}
	return func() {
		findProcessesByNameFunc = previous
	}
}
