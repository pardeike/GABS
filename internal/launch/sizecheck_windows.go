//go:build windows

package launch

func currentUnixExecLimits() (unixExecLimits, bool) {
	return unixExecLimits{}, false
}
