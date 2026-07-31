package bootstrap

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/luxuryprivate/switchboard/backend/internal/migration/pythonconfig"
	keydpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/dpapi"
	providerdpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/adapters/dpapi"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	routedpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/adapters/dpapi"
	tunneldpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/dpapi"
)

func migrateLegacySettings(logger *log.Logger) {
	for _, name := range []string{"SWITCHBOARD_PROVIDERS_PATH", "SWITCHBOARD_KEYS_PATH", "SWITCHBOARD_ROUTES_PATH", "SWITCHBOARD_TUNNEL_PATH"} {
		if os.Getenv(name) != "" {
			return
		}
	}
	providerPath, providerErr := providerdpapi.DefaultPath()
	keyPath, keyErr := keydpapi.DefaultPath()
	routePath, routeErr := routedpapi.DefaultPath()
	tunnelPath, tunnelErr := tunneldpapi.DefaultPath()
	if providerErr != nil || keyErr != nil || routeErr != nil || tunnelErr != nil || anyExists(providerPath, keyPath, routePath, tunnelPath) {
		return
	}
	legacy, found, err := pythonconfig.LoadDefault()
	if err != nil || !found {
		if err != nil {
			logger.Printf("legacy settings could not be imported")
		}
		return
	}
	ctx := context.Background()
	if err := providerdpapi.New(providerPath).Save(ctx, providerapp.SavedState{Providers: legacy.Providers, ActiveID: legacy.ActiveID}); err != nil {
		logger.Printf("legacy settings could not be imported")
		return
	}
	if err := keydpapi.New(keyPath).Save(ctx, legacy.Keys); err != nil {
		logger.Printf("legacy settings import was incomplete")
		return
	}
	if err := routedpapi.New(routePath).Save(ctx, legacy.Routes); err != nil {
		logger.Printf("legacy settings import was incomplete")
		return
	}
	if legacy.HasTunnel {
		if err := tunneldpapi.New(tunnelPath).Save(ctx, legacy.Tunnel); err != nil {
			logger.Printf("legacy settings import was incomplete")
			return
		}
	}
	logger.Printf("legacy settings imported into encrypted Go storage")
}

func anyExists(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}
