//go:build windows

package cloudflared

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps the connector's console window from flashing on a desktop
// launch.
func hideConsole(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
