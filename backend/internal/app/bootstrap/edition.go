package bootstrap

import (
	"context"
	"log"

	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	relayhttp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/http"
	routeapp "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	settingsdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
)

// The public edition ships without the publishing stack. Its wiring lives behind
// the "public" build tag, so the tunnel, its clients and shared control are absent
// from the binary instead of merely hidden in the interface: a public build cannot
// publish a gateway even when its sidecar is driven directly over stdio.
type editionDependencies struct {
	catalog  *providerapp.Catalog
	keys     *keyapp.Manager
	routes   *routeapp.Service
	relay    *relayhttp.Server
	settings settingsdomain.Settings
	logger   *log.Logger
}

// editionRuntime owns whatever lifecycle the edition added. Both hooks are nil when
// the edition contributes nothing.
type editionRuntime struct {
	stop  func(context.Context) error
	close func(context.Context) error
}

func (runtime editionRuntime) Stop(ctx context.Context) error {
	if runtime.stop == nil {
		return nil
	}
	return runtime.stop(ctx)
}

func (runtime editionRuntime) Close(ctx context.Context) error {
	if runtime.close == nil {
		return nil
	}
	return runtime.close(ctx)
}
