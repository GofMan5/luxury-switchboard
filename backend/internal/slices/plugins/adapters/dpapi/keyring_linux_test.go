//go:build linux

package dpapi

import "github.com/zalando/go-keyring"

func useTestKeyring() { keyring.MockInit() }
