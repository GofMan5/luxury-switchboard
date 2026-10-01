package domain

// ProxySchemeAllowed names the forwarding schemes a credential proxy may use.
// The relay's own transport, the key's write-time validation and the pool
// check all accept exactly these: the list lives once, where the proxy
// semantics live, so the three readers cannot drift apart silently. Same
// pattern as relayapp.MinRedactableMarkerBytes — a named width beats three
// hand-maintained copies of the same rule.
func ProxySchemeAllowed(scheme string) bool {
	switch scheme {
	case "http", "https", "socks5", "socks5h":
		return true
	default:
		return false
	}
}
