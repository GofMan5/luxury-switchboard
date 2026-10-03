//go:build !windows

package cloudflared

import "os/exec"

func hideConsole(*exec.Cmd) {}
