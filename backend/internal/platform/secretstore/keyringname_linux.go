//go:build linux

package secretstore

// keyringBackend names the OS keyring this build speaks, in the sentences the
// operator reads when it is missing or wrong.
const keyringBackend = "Linux Secret Service"
