//go:build !windows && !darwin && !linux

package launch

func currentUnixExecLimits() (unixExecLimits, bool) {
	return unixExecLimits{}, false
}
