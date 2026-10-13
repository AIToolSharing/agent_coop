package shim

import (
	"os/exec"
	"syscall"
)

// detach starts the worker in a process group of its own, with no console: a Ctrl-C on this
// console does not end it.
func detach(cmd *exec.Cmd) {
	const createNewProcessGroup, detachedProcess = 0x00000200, 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}
