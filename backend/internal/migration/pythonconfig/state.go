package pythonconfig

import (
	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	routedomain "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
	tunneldomain "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

type State struct {
	Providers []providerdomain.Provider
	ActiveID  string
	Keys      []keydomain.Key
	Routes    []routedomain.Assignment
	Tunnel    tunneldomain.Config
	HasTunnel bool
}
