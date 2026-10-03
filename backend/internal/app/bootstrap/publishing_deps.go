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

// publishingDependencies is what the publishing stack (tunnel gateway, client
// governance) needs from the rest of the composition.
type editionDependencies struct {
	catalog  *providerapp.Catalog
	keys     *keyapp.Manager
	routes   *routeapp.Service
	relay    *relayhttp.Server
	settings settingsdomain.Settings
	// applySettings lets the publishing stores follow live settings changes;
	// nil in a wiring that has none to follow.
	applySettings func(listener func(settingsdomain.Settings))
	logger        *log.Logger
}

// editionRuntime owns whatever lifecycle the publishing stack added. Both hooks
// are nil when it contributes nothing.
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
