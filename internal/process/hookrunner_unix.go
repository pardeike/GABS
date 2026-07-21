//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// configureHookProcessGroup puts the hook in its own process group so a
// timeout kill reaches the whole tree (design/01).
func configureHookProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// hookTree targets the hook's process group; with Setpgid the group ID is
// the direct child's PID.
type hookTree struct {
	pgid int
}

func newHookTree(cmd *exec.Cmd) (*hookTree, error) {
	return &hookTree{pgid: cmd.Process.Pid}, nil
}

func (t *hookTree) kill() error {
	return syscall.Kill(-t.pgid, syscall.SIGKILL)
}

func (t *hookTree) close() {}
