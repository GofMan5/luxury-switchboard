//go:build !windows && !linux

package openssh

import "os/exec"

func hideCommand(*exec.Cmd) {}
