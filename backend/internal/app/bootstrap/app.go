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
	activitysqlite "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/adapters/sqlite"
	activitystdio "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/adapters/stdio"
	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	guardrailrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/relay"
	guardrailruleset "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
	guardrailstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/stdio"
	guardrailapp "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	keydpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/dpapi"
	keystdio "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/stdio"
	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	modelproviders "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/providers"
	modelrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/relay"
	modelstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/stdio"
	modelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/models/application"
	providerdpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/adapters/dpapi"
	providerstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/adapters/stdio"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
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
	settingsdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
	systemstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/system/adapters/stdio"
)

type App struct {
	protocol *platform.Server
	relay    *relayapp.Service
	history  activityapp.History
	edition  editionRuntime
	logger   *log.Logger
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
	providerManager.SetRouteUsage(routeService)
	routes := relayproviders.NewSource(catalog, relayroutes.NewResolver(routeService))
	credentials := relaykeypool.NewSource(keyScheduler)
	activity := activityapp.NewService(settings.ActivityCapacity)
	history, historyErr := defaultHistory(settings.HistoryRetentionDays)
	if historyErr != nil {
		logger.Printf("request history is unavailable; relay will continue")
	}
	recorder := relayactivity.NewRecorder(activity)
	guardrails, err := defaultGuardrails(settings)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(environmentPort(settings.ListenerPort)))
	httpRuntime := relayhttp.NewServer(address, relayhttp.Dependencies{
		Routes: routes, Credentials: credentials, Activity: recorder,
		Guardrail: guardrailrelay.New(guardrails),
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
	guardrailstdio.Register(protocol, guardrails)
	// The mode is a setting, so changing it must take effect on the next request
	// rather than at the next launch.
	settingsService.OnApplied(func(applied settingsdomain.Settings) {
		if mode, err := guardraildomain.ParseMode(applied.Normalized().GuardrailMode); err == nil {
			_ = guardrails.SetMode(mode)
		}
	})
	routestdio.Register(protocol, routeService)
	modelstdio.Register(protocol, modelService)
	edition, err := registerEdition(protocol, editionDependencies{
		catalog: catalog, keys: keyManager, routes: routeService,
		relay: httpRuntime, settings: settings, logger: logger,
	})
	if err != nil {
		return nil, err
	}
	return &App{
		protocol: protocol,
		relay:    relay,
		history:  history,
		edition:  edition,
		logger:   logger,
	}, nil
}

func (app *App) Run(ctx context.Context) error {
	if _, err := app.relay.Start(); err != nil {
		app.logger.Printf("relay start failed")
	}
	err := app.protocol.Serve(ctx)
	editionCtx, cancelEdition := context.WithTimeout(context.Background(), 5*time.Second)
	if stopErr := app.edition.Stop(editionCtx); stopErr != nil {
		app.logger.Printf("edition runtime did not stop cleanly")
	}
	cancelEdition()
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
	editionHistoryCtx, cancelEditionHistory := context.WithTimeout(context.Background(), 5*time.Second)
	if historyErr := app.edition.Close(editionHistoryCtx); historyErr != nil {
		app.logger.Printf("edition history did not close cleanly")
	}
	cancelEditionHistory()
	return err
}

func defaultGuardrails(settings settingsdomain.Settings) (*guardrailapp.Inspector, error) {
	engine, err := guardraildomain.NewEngine(guardrailruleset.RulesJSON, guardrailruleset.BlocklistJSON)
	if err != nil {
		return nil, err
	}
	normalized := settings.Normalized()
	mode, err := guardraildomain.ParseMode(normalized.GuardrailMode)
	if err != nil {
		return nil, err
	}
	return guardrailapp.NewInspector(engine, mode, normalized.GuardrailFindings)
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
		CacheTTL: time.Hour, ImageCompat: true, Enabled: true, Builtin: true,
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
	rates := make(map[string]keyapp.Rate, len(providers))
	for _, provider := range providers {
		rates[provider.ID] = keyapp.Rate{Limit: provider.RPM, Window: provider.RateWindow()}
	}
	path := os.Getenv("SWITCHBOARD_KEYS_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = keydpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, nil, errors.New("key settings path must be absolute")
	}
	repository := keydpapi.New(path)
	manager, err := keyapp.NewManager(scheduler, repository, rates, nil)
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
	// The concrete store is unwrapped so a failure returns a truly nil interface;
	// otherwise every downstream nil guard would pass on a nil pointer.
	store, err := activitysqlite.Open(path, retentionDays)
	if err != nil {
		return nil, err
	}
	return store, nil
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
