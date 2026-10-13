//go:build !windows

package shim

import (
	"os/exec"
	"syscall"
)

// detach puts the worker in a session of its own: a signal to this process's group does not
// end it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
