//go:build !public

package systemstdio

// Edition names the build so clients can present exactly the workspaces this
// binary serves.
const Edition = "owner"

var editionCapabilities = []string{"tunnel.manage", "clients.manage", "shared.control"}
