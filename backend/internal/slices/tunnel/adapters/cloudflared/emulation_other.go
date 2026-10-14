//go:build !windows

package cloudflared

func x64EmulationSupported() (bool, error) { return false, nil }
