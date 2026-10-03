package systemstdio

// Edition names the build. There is one build — the full product — and the
// field stays on the wire because old shells may still read it.
const Edition = "full"

var editionCapabilities = []string{"tunnel.manage", "clients.manage"}
