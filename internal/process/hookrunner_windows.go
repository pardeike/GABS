//go:build windows

package process

import (
	"os/exec"
	"syscall"
)

var (
	hookKernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = hookKernel32.NewProc("CreateJobObjectW")
	procAssignProcessToJobObject = hookKernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = hookKernel32.NewProc("TerminateJobObject")
)

const processSetQuota = 0x0100 // required by AssignProcessToJobObject

func configureHookProcessGroup(cmd *exec.Cmd) {}

// hookTree is a Job Object holding the hook's process tree (design/20):
// children spawned after assignment are contained, and TerminateJobObject
// takes the whole tree down on timeout. Assignment happens immediately
// after Start — the microseconds before it are part of the documented
// residual-straggler window.
type hookTree struct {
	job  syscall.Handle
	proc syscall.Handle
}

func newHookTree(cmd *exec.Cmd) (*hookTree, error) {
	job, _, callErr := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return nil, callErr
	}
	proc, err := syscall.OpenProcess(processSetQuota|syscall.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		syscall.CloseHandle(syscall.Handle(job))
		return nil, err
	}
	if r, _, callErr := procAssignProcessToJobObject.Call(job, uintptr(proc)); r == 0 {
		syscall.CloseHandle(syscall.Handle(job))
		syscall.CloseHandle(proc)
		return nil, callErr
	}
	return &hookTree{job: syscall.Handle(job), proc: proc}, nil
}

func (t *hookTree) kill() error {
	if r, _, callErr := procTerminateJobObject.Call(uintptr(t.job), 1); r == 0 {
		return callErr
	}
	return nil
}

func (t *hookTree) close() {
	syscall.CloseHandle(t.job)
	syscall.CloseHandle(t.proc)
}
