//go:build windows

package openssh

import (
	"os/exec"
	"syscall"
)

func hideCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
