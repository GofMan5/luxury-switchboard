package bootstrap

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/userenv"
	activitysqlite "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/adapters/sqlite"
	activitystdio "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/adapters/stdio"
	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	keydpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/dpapi"
	keystdio "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/stdio"
	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	modelproviders "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/providers"
	modelrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/relay"
	modelstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/stdio"
	modelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/models/application"
	providerdpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/adapters/dpapi"
	providerstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/adapters/stdio"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	publicactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/adapters/activity"
	publicmarkers "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/adapters/providers"
	publicroutes "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/adapters/routes"
	relayactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/activity"
	relayhttp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/http"
	relaykeypool "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/keypool"
	relayproviders "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/providers"
	relayroutes "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/routes"
	relaystdio "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/stdio"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	routedpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/adapters/dpapi"
	routeproviders "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/adapters/providers"
	routestdio "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/adapters/stdio"
	routeapp "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	routedomain "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
	settingsdpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/adapters/dpapi"
	settingsstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/adapters/stdio"
	settingsapp "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/application"
	sharedssh "github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/adapters/ssh"
	sharedstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/adapters/stdio"
	sharedapp "github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/application"
	shareddomain "github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/domain"
	systemstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/system/adapters/stdio"
	tunneldpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/dpapi"
	tunnelhttp "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/http"
	tunnelroutes "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/routes"
	tunnelssh "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/ssh"
	tunnelstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/adapters/stdio"
	tunnelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/application"
	clientsqlite "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/adapters/sqlite"
	clientstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/adapters/stdio"
	clientapp "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/application"
)

type App struct {
	protocol      *platform.Server
	relay         *relayapp.Service
	history       activityapp.History
	tunnelHistory clientapp.History
	tunnel        *tunnelapp.Service
	logger        *log.Logger
}

func New(stdin io.Reader, stdout io.Writer, stderr io.Writer) (*App, error) {
	logger := log.New(stderr, "switchboard: ", log.LstdFlags|log.Lmsgprefix)
	settingsService, settingsLoadErr, err := defaultSettingsService()
	if err != nil {
		return nil, err
	}
	if settingsLoadErr != nil {
		logger.Printf("encrypted settings could not be loaded; using safe defaults")
	}
	migrateLegacySettings(logger)
	settings := settingsService.Snapshot()
	providers, activeID, err := defaultProviders()
	if err != nil {
		return nil, err
	}
	catalog, err := providerapp.NewCatalog(providers, activeID)
	if err != nil {
		return nil, err
	}
	keyScheduler, keyManager, keyPathErr, err := defaultKeyManager(providers, settings.MaxQueued)
	if err != nil {
		return nil, err
	}
	providerManager, providerLoadErr, err := defaultProviderManager(catalog, keyManager)
	if err != nil {
		return nil, err
	}
	if providerLoadErr != nil {
		logger.Printf("encrypted provider settings could not be loaded; using runtime defaults")
	}
	keyLoadErr := keyPathErr
	if keyLoadErr == nil {
		keyLoadErr = keyManager.Load(context.Background())
	}
	if keyLoadErr != nil {
		logger.Printf("encrypted key settings could not be loaded; using runtime defaults")
	}
	routeService, routeLoadErr, err := defaultRouteService(catalog)
	if err != nil {
		return nil, err
	}
	if routeLoadErr != nil {
		logger.Printf("encrypted model routes could not be loaded; using active provider")
	}
	routes := relayproviders.NewSource(catalog, relayroutes.NewResolver(routeService))
	credentials := relaykeypool.NewSource(keyScheduler)
	activity := activityapp.NewService(settings.ActivityCapacity)
	history, historyErr := defaultHistory(settings.HistoryRetentionDays)
	if historyErr != nil {
		logger.Printf("request history is unavailable; relay will continue")
	}
	recorder := relayactivity.NewRecorder(activity)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(environmentPort(settings.ListenerPort)))
	httpRuntime := relayhttp.NewServer(address, relayhttp.Dependencies{
		Routes: routes, Credentials: credentials, Activity: recorder,
		Config: relayhttp.Config{
			MaxRequestBytes:       int64(settings.MaxRequestMiB) * 1024 * 1024,
			ResponseHeaderTimeout: time.Duration(settings.HeaderTimeoutSeconds) * time.Second,
			StreamIdleTimeout:     time.Duration(settings.StreamIdleSeconds) * time.Second,
			RetryBase:             time.Duration(settings.RetryBaseMilliseconds) * time.Millisecond,
			RetryMax:              time.Duration(settings.RetryMaxSeconds) * time.Second,
			PermanentAttempts:     settings.PermanentAttempts,
		},
	})
	modelService, err := modelapp.NewService(
		modelproviders.NewCatalog(catalog),
		modelrelay.NewGateway(httpRuntime, max(time.Duration(settings.HeaderTimeoutSeconds)*time.Second, 90*time.Second)),
	)
	if err != nil {
		return nil, err
	}
	publicProviderPolicy := publicmarkers.NewMarkers(catalog, keyManager)
	publicRouteSource := publicroutes.NewSource(routeService, publicProviderPolicy)
	sharedControl, err := sharedapp.NewService(sharedssh.NewClient())
	if err != nil {
		return nil, err
	}
	tunnelHistory, tunnelHistoryErr := defaultTunnelHistory(settings.TunnelRetentionHours)
	if tunnelHistoryErr != nil {
		logger.Printf("tunnel history is unavailable; live client activity will continue")
	}
	clients := clientapp.NewService(tunnelHistory)
	localTunnelRuntime := tunnelhttp.NewRuntime(publicRouteSource, publicProviderPolicy, httpRuntime, publicactivity.NewRecorder(clients))
	tunnelRuntime := tunnelssh.NewRuntime(localTunnelRuntime, publicRouteSource, func(ctx context.Context) error {
		_, err := sharedControl.EnsureSelfRunning(ctx)
		return err
	})
	tunnelService, tunnelLoadErr, err := defaultTunnelService(tunnelRuntime, tunnelroutes.NewSource(publicRouteSource))
	if err != nil {
		return nil, err
	}
	if tunnelLoadErr != nil {
		logger.Printf("encrypted tunnel settings could not be loaded; tunnel remains stopped")
	}
	sharedControl.OnChanged(func(snapshot shareddomain.Snapshot) {
		for _, tunnel := range snapshot.Tunnels {
			if tunnel.Name == sharedapp.SelfName {
				tunnelService.SetPublicationState(tunnel.State)
				return
			}
		}
	})
	relay := relayapp.NewService(httpRuntime)
	catalog.OnActivated(func(string) { relay.CancelActive() })
	routeService.OnChanged(func(target routedomain.Target) {
		if target == routedomain.TargetRelay {
			relay.CancelActive()
		}
	})

	protocol := platform.NewServer(stdin, stdout, 32)
	systemstdio.Register(protocol)
	providerstdio.Register(protocol, catalog, providerManager, keyScheduler)
	keystdio.Register(protocol, keyManager)
	relaystdio.Register(protocol, relay)
	activitystdio.Register(protocol, activity, history)
	settingsstdio.Register(protocol, settingsService)
	routestdio.Register(protocol, routeService)
	modelstdio.Register(protocol, modelService)
	sharedstdio.Register(protocol, sharedControl)
	tunnelstdio.Register(protocol, tunnelService)
	clientstdio.Register(protocol, clients)
	return &App{
		protocol:      protocol,
		relay:         relay,
		history:       history,
		tunnelHistory: tunnelHistory,
		tunnel:        tunnelService,
		logger:        logger,
	}, nil
}

func (app *App) Run(ctx context.Context) error {
	if _, err := app.relay.Start(); err != nil {
		app.logger.Printf("relay start failed")
	}
	err := app.protocol.Serve(ctx)
	if app.tunnel != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = app.tunnel.Stop(stopCtx)
		cancel()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if stopErr := app.relay.Stop(stopCtx); stopErr != nil {
		app.logger.Printf("relay shutdown was not clean")
	}
	cancel()
	if app.history != nil {
		historyCtx, cancelHistory := context.WithTimeout(context.Background(), 5*time.Second)
		if historyErr := app.history.Close(historyCtx); historyErr != nil {
			app.logger.Printf("request history did not close cleanly")
		}
		cancelHistory()
	}
	if app.tunnelHistory != nil {
		historyCtx, cancelHistory := context.WithTimeout(context.Background(), 5*time.Second)
		if historyErr := app.tunnelHistory.Close(historyCtx); historyErr != nil {
			app.logger.Printf("tunnel history did not close cleanly")
		}
		cancelHistory()
	}
	return err
}

func defaultProviders() ([]providerdomain.Provider, string, error) {
	local, err := providerdomain.New(providerdomain.Params{
		ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8799",
		AuthMode: providerdomain.AuthPassthrough, Enabled: true, Builtin: true,
	})
	if err != nil {
		return nil, "", err
	}
	echo, err := providerdomain.New(providerdomain.Params{
		ID: "echo", Name: "EchoGate", BaseURL: "https://api.echogate.one/v1",
		AuthMode: providerdomain.AuthAuto,
		RPM:      environmentBounded("SWITCHBOARD_ECHO_RPM", 0, providerdomain.MaxRPM),
		CacheTTL: time.Hour, Enabled: true, Builtin: true,
	})
	if err != nil {
		return nil, "", err
	}
	active := os.Getenv("SWITCHBOARD_PROVIDER")
	if active != "local" && active != "echo" {
		active = "local"
	}
	return []providerdomain.Provider{local, echo}, active, nil
}

func defaultKeyManager(providers []providerdomain.Provider, maxQueued int) (*keyapp.Scheduler, *keyapp.Manager, error, error) {
	scheduler := keyapp.NewScheduler(maxQueued)
	builtins := make([]keydomain.Key, 0, 1)
	rates := make(map[string]int, len(providers))
	for _, provider := range providers {
		rates[provider.ID] = provider.RPM
		if provider.ID == "echo" {
			if secret := userenv.Get("FREEMODEL_API_KEY"); secret != "" {
				key, err := keydomain.NewKey(keydomain.Params{
					ProviderID: provider.ID,
					Label:      "Environment key",
					Secret:     secret,
					Priority:   0,
					RPM:        environmentBounded("SWITCHBOARD_LITE_RPM", 30, keydomain.MaxRPM),
					Pinned:     true,
				})
				if err != nil {
					return nil, nil, nil, err
				}
				builtins = append(builtins, key)
			}
		}
	}
	path := os.Getenv("SWITCHBOARD_KEYS_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = keydpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, nil, errors.New("key settings path must be absolute")
	}
	repository := keydpapi.New(path)
	manager, err := keyapp.NewManager(scheduler, repository, rates, builtins)
	if err != nil {
		return nil, nil, nil, err
	}
	return scheduler, manager, pathErr, nil
}

func defaultProviderManager(catalog *providerapp.Catalog, keys *keyapp.Manager) (*providerapp.Manager, error, error) {
	path := os.Getenv("SWITCHBOARD_PROVIDERS_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = providerdpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, errors.New("provider settings path must be absolute")
	}
	manager, err := providerapp.NewManager(catalog, providerdpapi.New(path), keys)
	if err != nil {
		return nil, nil, err
	}
	loadErr := pathErr
	if loadErr == nil {
		loadErr = manager.Load(context.Background())
	}
	return manager, loadErr, nil
}

func defaultSettingsService() (*settingsapp.Service, error, error) {
	path := os.Getenv("SWITCHBOARD_SETTINGS_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = settingsdpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, errors.New("settings path must be absolute")
	}
	service, err := settingsapp.NewService(settingsdpapi.New(path))
	if err != nil {
		return nil, nil, err
	}
	loadErr := pathErr
	if loadErr == nil {
		loadErr = service.Load(context.Background())
	}
	return service, loadErr, nil
}

func defaultHistory(retentionDays int) (activityapp.History, error) {
	path := os.Getenv("SWITCHBOARD_HISTORY_PATH")
	var err error
	if path == "" {
		path, err = activitysqlite.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, errors.New("history path must be absolute")
	}
	if err != nil {
		return nil, err
	}
	return activitysqlite.Open(path, retentionDays)
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
	return clientsqlite.Open(path, retentionHours)
}

func defaultRouteService(catalog *providerapp.Catalog) (*routeapp.Service, error, error) {
	path := os.Getenv("SWITCHBOARD_ROUTES_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = routedpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, errors.New("routes path must be absolute")
	}
	service, err := routeapp.NewService(routedpapi.New(path), routeproviders.NewCatalog(catalog))
	if err != nil {
		return nil, nil, err
	}
	loadErr := pathErr
	if loadErr == nil {
		loadErr = service.Load(context.Background())
	}
	return service, loadErr, nil
}

func defaultTunnelService(runtime tunnelapp.Runtime, routes tunnelapp.Routes) (*tunnelapp.Service, error, error) {
	path := os.Getenv("SWITCHBOARD_TUNNEL_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = tunneldpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, errors.New("tunnel path must be absolute")
	}
	service, err := tunnelapp.NewService(tunneldpapi.New(path), runtime, routes)
	if err != nil {
		return nil, nil, err
	}
	loadErr := pathErr
	if loadErr == nil {
		loadErr = service.Load(context.Background())
	}
	return service, loadErr, nil
}

func environmentPort(fallback int) int {
	return environmentBounded("SWITCHBOARD_PORT", fallback, 65535)
}

func environmentBounded(name string, fallback, maximum int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > maximum {
		return fallback
	}
	return value
}
