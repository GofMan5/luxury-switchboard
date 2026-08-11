//go:build linux

package openssh

import (
	"os/exec"
	"syscall"
)

func hideCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
