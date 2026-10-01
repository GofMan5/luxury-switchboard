//go:build !public

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	publicactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/adapters/activity"
	publicmarkers "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/adapters/providers"
	publicroutes "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/adapters/routes"
	sharedssh "github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/adapters/ssh"
	sharedstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/adapters/stdio"
	sharedapp "github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/application"
	shareddomain "github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/domain"
	tunneldpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/dpapi"
	tunnelhttp "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/http"
	tunnelprivacy "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/privacyaudit"
	tunnelroutes "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/routes"
	tunnelssh "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/ssh"
	tunnelstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/stdio"
	tunnelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/application"
	clientsqlite "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/adapters/sqlite"
	clientstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/adapters/stdio"
	clientapp "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/application"
)

// registerEdition wires the owner-only publishing stack: the public gateway, its
// per-client governance and shared control.
func registerEdition(protocol *platform.Server, dependencies editionDependencies) (editionRuntime, error) {
	logger := dependencies.logger
	markers := publicmarkers.NewMarkers(dependencies.catalog, dependencies.keys)
	routes := publicroutes.NewSource(dependencies.routes, markers)
	sharedControl, err := sharedapp.NewService(sharedssh.NewClient())
	if err != nil {
		return editionRuntime{}, err
	}
	history, historyErr := defaultTunnelHistory(dependencies.settings.TunnelRetentionHours)
	if historyErr != nil {
		// Bans live in the same store, so this is not only a telemetry gap: every
		// decision from an earlier session is unreachable and a new one cannot be
		// saved. The message says so, because the two are not the same loss.
		logger.Printf("tunnel client storage is unavailable; earlier bans are not in force and new ones cannot be saved")
	}
	clients := clientapp.NewService(history)
	if err := clients.LoadProfiles(context.Background()); err != nil && historyErr == nil {
		// The report carries the number of rows that could not be read: one
		// dropped ban and fifty are different mornings, and the readable bans
		// are in force either way — the message says so.
		logger.Printf("tunnel client bans could not all be restored: %v; readable bans are in force", err)
	}
	gateway := tunnelhttp.NewRuntime(routes, markers, dependencies.relay, publicactivity.NewRecorder(clients), clients)
	publisher := tunnelssh.NewRuntime(gateway, routes, func(ctx context.Context) error {
		_, err := sharedControl.EnsureSelfRunning(ctx)
		return err
	})
	service, loadErr, err := defaultTunnelService(publisher, tunnelroutes.NewSource(routes))
	if err != nil {
		return editionRuntime{}, err
	}
	if loadErr != nil {
		logger.Printf("encrypted tunnel settings could not be loaded; tunnel remains stopped")
	}
	sharedControl.OnChanged(func(snapshot shareddomain.Snapshot) {
		for _, tunnel := range snapshot.Tunnels {
			if tunnel.Name == sharedapp.SelfName {
				service.SetPublicationState(tunnel.State)
				return
			}
		}
	})
	sharedstdio.Register(protocol, sharedControl)
	tunnelstdio.Register(protocol, service)
	clientstdio.Register(protocol, clients)
	runtime := editionRuntime{stop: service.Stop}
	if history != nil {
		runtime.close = history.Close
	}
	return runtime, nil
}

func defaultTunnelService(runtime tunnelapp.Runtime, routes tunnelapp.Routes) (*tunnelapp.Service, error, error) {
	path := os.Getenv("SWITCHBOARD_TUNNEL_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = tunneldpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, errors.New("tunnel path must be absolute")
	}
	service, err := tunnelapp.NewService(tunneldpapi.New(path), runtime, routes, tunnelprivacy.NewClient())
	if err != nil {
		return nil, nil, err
	}
	loadErr := pathErr
	if loadErr == nil {
		loadErr = service.Load(context.Background())
	}
	return service, loadErr, nil
}

func defaultTunnelHistory(retentionHours int) (clientapp.History, error) {
	path := os.Getenv("SWITCHBOARD_TUNNEL_HISTORY_PATH")
	var err error
	if path == "" {
		path, err = clientsqlite.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, errors.New("tunnel history path must be absolute")
	}
	if err != nil {
		return nil, err
	}
	// The concrete store is unwrapped so a failure returns a truly nil interface;
	// otherwise every downstream nil guard would pass on a nil pointer.
	store, err := clientsqlite.Open(path, retentionHours)
	if err != nil {
		return nil, err
	}
	return store, nil
}
