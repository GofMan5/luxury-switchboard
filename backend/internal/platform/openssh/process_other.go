//go:build !windows

package openssh

import "os/exec"

func hideCommand(*exec.Cmd) {}
