//go:build windows

package dpapi

// DPAPI needs no test keyring.
func useTestKeyring() {}
