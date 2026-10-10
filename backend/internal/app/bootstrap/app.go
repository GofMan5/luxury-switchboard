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
	analyticsjsonfile "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/adapters/jsonfile"
	analyticssqlite "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/adapters/sqlite"
	analyticsstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/adapters/stdio"
	analyticsapp "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/application"
	backuplive "github.com/luxuryprivate/switchboard/backend/internal/slices/backup/adapters/live"
	backupstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/backup/adapters/stdio"
	backupapp "github.com/luxuryprivate/switchboard/backend/internal/slices/backup/application"
	codeximports "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/adapters/imports"
	codexloopback "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/adapters/loopback"
	codexoauth "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/adapters/oauth"
	codexproviders "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/adapters/providers"
	codexrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/adapters/relay"
	codexstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/adapters/stdio"
	codexstore "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/adapters/store"
	codexapplication "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	guardrailrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/relay"
	guardrailruleset "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
	guardrailstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/stdio"
	guardrailapp "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	keydpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/dpapi"
	keyprobe "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/probe"
	keystdio "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/stdio"
	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	modelproviders "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/providers"
	modelrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/relay"
	modelstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/models/adapters/stdio"
	modelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/models/application"
	notificationsrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/adapters/relay"
	notificationsstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/adapters/stdio"
	notificationsapp "github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/application"
	notificationsdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/domain"
	providerdpapi "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/adapters/dpapi"
	providerhealth "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/adapters/health"
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
	relaydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
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
	updategithub "github.com/luxuryprivate/switchboard/backend/internal/slices/updates/adapters/github"
	updatesstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/updates/adapters/stdio"
	updatesapp "github.com/luxuryprivate/switchboard/backend/internal/slices/updates/application"
)

type App struct {
	protocol         *platform.Server
	relay            *relayapp.Service
	history          activityapp.History
	analytics        *analyticssqlite.Facts
	publishing       editionRuntime
	logger           *log.Logger
	healthMonitor    *providerapp.HealthMonitor
	healthCancel     context.CancelFunc
	updatesRefresher *updatesapp.Refresher
	updatesCancel    context.CancelFunc
	codex            *codexapplication.Service
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
	keyScheduler, keyManager, keyRepository, keyPathErr, err := defaultKeyManager(providers, settings.MaxQueued)
	if err != nil {
		return nil, err
	}
	providerManager, providerRepository, providerLoadErr, err := defaultProviderManager(catalog, keyManager)
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
	routeService, routeRepository, routeLoadErr, err := defaultRouteService(catalog)
	if err != nil {
		return nil, err
	}
	if routeLoadErr != nil {
		logger.Printf("encrypted model routes could not be loaded; using active provider")
	}
	providerManager.SetRouteCascade(routeService)
	routeResolver := relayroutes.NewResolver(routeService)
	routes := relayproviders.NewSource(catalog, routeResolver, routeResolver)
	// The codex route must carry the identity headers the reference client
	// sends (User-Agent "Codex Desktop/<version>", originator), and the
	// version travels with the Go AppVersion the build stamped.
	routes.SetAppVersion(systemstdio.AppVersion)
	// The codex provider is the one provider that does not authenticate with
	// a pooled key: it signs in with ChatGPT's OAuth and refreshes its own
	// session, DPAPI-encrypted at its own path. The authorizer takes a nil
	// client on purpose — every call it makes is bounded by a context the
	// application layer already holds — and the loopback redirect server
	// opens its listener only while a login is actually in flight.
	codexDir, codexLegacyPath, codexPathErr, err := defaultCodexStore()
	if err != nil {
		return nil, err
	}
	if codexPathErr != nil {
		logger.Printf("codex account storage could not be located; sign-ins will not persist")
	}
	codexService := codexapplication.NewService(
		codexstore.NewRepository(codexDir, codexLegacyPath),
		codexoauth.NewAuthorizer(nil, systemstdio.AppVersion),
		codexloopback.NewRedirectServer(),
		codexproviders.NewProvisioner(providerManager),
		codeximports.NewParser(),
		codeximports.NewFileReader(),
		nil,
	)
	// The relay's credentials are a composite: codex traffic draws its token
	// from the OAuth session, everything else keeps drawing from the key
	// pool, and a codex request never falls back to a key — there is no key
	// to fall back to.
	credentials := codexrelay.NewCompositeSource(
		codexrelay.NewTokenSource(codexService),
		relaykeypool.NewSource(keyScheduler),
	)
	activity := activityapp.NewService(settings.ActivityCapacity)
	notifications := notificationsapp.NewService()
	// A session that slid into reauth-needed is the codex provider's dead-key
	// event: the pool cannot rotate around it, so the operator is the one who
	// has to act. Accounts are plural, so the edge is per account — one
	// expiring session must warn even while its siblings stay signed in. The
	// notification fires on the transition only; the watcher owns that edge.
	// The text stays generic on purpose: the notification feed carries no
	// account identifiers.
	codexReauths := newCodexReauthWatcher()
	codexService.OnChanged(func(snapshot codexapplication.Snapshot) {
		if len(codexReauths.observe(snapshot.Conn.Accounts)) > 0 {
			_ = notifications.Raise(
				notificationsdomain.KindCodexAuth, notificationsdomain.SeverityWarning,
				"Codex sign-in is needed",
				"A ChatGPT session expired. Sign in again from the Codex accounts to keep the provider working.",
			)
		}
	})
	// History is the operator's record of what happened, and a storage outage
	// used to stop it in silence: the queue filled, records dropped, and the
	// only symptom was a journal that quietly stopped growing. The store says
	// so through the notification feed — the channel that exists for exactly
	// this — in words that name the price, not the file. Each cause carries
	// its own title, because the feed deduplicates on kind+title and one
	// outage reliably produces both.
	history, historyPath, historyErr := defaultHistory(settings.HistoryRetentionDays, func(cause string) {
		title, body := "Request history is not being persisted",
			"The history database refused a write and the records are queued for retry. If this keeps repeating, the journal is not being persisted."
		if cause == activitysqlite.CauseQueueFull {
			title, body = "Request history is dropping new records",
				"Requests arrived faster than the history database could file them, and the newest records were dropped. Live activity is unaffected; the persisted journal is missing these rows."
		}
		_ = notifications.Raise(notificationsdomain.KindHistoryDrop, notificationsdomain.SeverityWarning, title, body)
	})
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
		Guardrail: guardrailrelay.New(guardrails), Failovers: routes,
		RouteEvents: notificationsrelay.NewSink(notifications),
		// The learned request shapes persist beside the history database:
		// a probe 400 is a full round trip on the first request of every
		// launch otherwise. The file carries no secret.
		RepairMemoPath: repairMemoPath(historyPath),
		Config: relayhttp.Config{
			MaxRequestBytes:       int64(settings.MaxRequestMiB) * 1024 * 1024,
			ResponseHeaderTimeout: time.Duration(settings.HeaderTimeoutSeconds) * time.Second,
			StreamIdleTimeout:     time.Duration(settings.StreamIdleSeconds) * time.Second,
			HeartbeatInterval:     time.Duration(settings.HeartbeatSeconds) * time.Second,
			LiveStreamProbation:   time.Duration(settings.StreamProbationMilliseconds) * time.Millisecond,
			RetryBase:             time.Duration(settings.RetryBaseMilliseconds) * time.Millisecond,
			RetryMax:              time.Duration(settings.RetryMaxSeconds) * time.Second,
			PermanentAttempts:     settings.PermanentAttempts,
		},
	})
	modelGateway := modelrelay.NewGateway(httpRuntime, max(time.Duration(settings.HeaderTimeoutSeconds)*time.Second, 90*time.Second))
	modelService, err := modelapp.NewService(
		modelproviders.NewCatalog(catalog),
		modelGateway,
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
	// Event delivery failures ride the same stderr the rest of the app logs
	// to: the shell drops them either way, but the owner's console gets the
	// word instead of a feed that quietly went quiet.
	protocol.Diagnostics(stderr)
	systemstdio.Register(protocol)
	// The health monitor probes enabled providers on an interval the settings
	// gate. A user's providers die the same
	// way an owner's do.
	healthMonitor, err := providerapp.NewHealthMonitor(catalog, providerhealth.NewHTTPProber())
	if err != nil {
		return nil, err
	}
	healthMonitor.SetEnabled(settings.ProviderHealthEnabled)
	providerstdio.Register(protocol, catalog, providerManager, keyScheduler, healthMonitor)
	// The codex provider's sign-in lives beside the provider commands: it is
	// the one provider whose credential is an OAuth session rather than a
	// key. Registration also wires the protocol's own codex.changed event,
	// so the frontend sees login and refresh transitions as they happen.
	codexstdio.Register(protocol, codexService)
	// The update check asks the project's own release feed, nothing else; a
	// quiet answer on a machine without network is the correct one. The
	// refresher owns the cadence the settings chose — one minute by default
	// — and announces verdict changes through the protocol's events, so
	// "update available" turns up on the minute the feed publishes it.
	updatesService := updatesapp.NewService(systemstdio.AppVersion, updategithub.NewClient())
	updatesRefresher := updatesapp.NewRefresher(updatesService)
	updatesRefresher.SetInterval(updatesapp.IntervalFromSetting(settings.Normalized().UpdateCheckInterval))
	updatesstdio.Register(protocol, updatesService, updatesRefresher)
	keystdio.Register(protocol, keyManager, keyprobe.NewProber(catalog))
	relaystdio.Register(protocol, relay)
	activitystdio.Register(protocol, activity, history)
	// Analytics reads the same database the history slice writes: the
	// composition root is the one place that knows they share a file. When
	// history could not open, analytics answers unavailable rather than
	// drawing a dashboard of zeros.
	var analyticsFacts *analyticssqlite.Facts
	if historyErr == nil {
		analyticsFacts, err = analyticssqlite.Open(historyPath)
		if err != nil {
			analyticsFacts = nil
			logger.Printf("analytics is unavailable; request history is unaffected")
		}
	}
	// The interface must stay a true nil when facts are absent: a typed nil
	// pointer would pass the service's unavailable guard and crash on use.
	var factsPort analyticsapp.Facts
	if analyticsFacts != nil {
		factsPort = analyticsFacts
	}
	analyticsPricePath, err := analyticsjsonfile.DefaultPath()
	if err != nil {
		return nil, err
	}
	priceStore := analyticsjsonfile.New(analyticsPricePath)
	analyticsService := analyticsapp.NewService(factsPort, priceStore, providerNames{catalog: catalog})
	analyticsstdio.Register(protocol, analyticsService)
	// The backup reads the same encrypted stores the managers own and restores
	// through them; the analytics price catalog travels with it, so a restore
	// on a new machine does not leave the cost estimate blind.
	backupService, err := backupapp.NewService(
		backuplive.NewSources(providerRepository, keyRepository, routeRepository, priceStore),
		backuplive.NewSinks(providerManager, keyManager, routeService, catalog, analyticsService),
	)
	if err != nil {
		return nil, err
	}
	backupstdio.Register(protocol, backupService)
	settingsstdio.Register(protocol, settingsService)
	guardrailstdio.Register(protocol, guardrails)
	notificationsstdio.Register(protocol, notifications)
	// A dead key is the one failure the relay absorbs silently — the pool
	// rotates, the request succeeds — so the operator learns it only here.
	keyScheduler.OnDeadKey(func(_, label string) {
		_ = notifications.Raise(
			notificationsdomain.KindKeyHealth, notificationsdomain.SeverityDanger,
			"Key \""+label+"\" looks dead",
			"It answered three authentication refusals in a row. Revoke it at the provider and replace it; the pool is working around it until then.",
		)
	})
	// Reachability transitions are notifications, not polls: the sidebar dot
	// carries the state, the toast carries the change.
	healthMonitor.OnChanged(func(changed providerapp.HealthState, _ []providerapp.HealthState) {
		provider, exists := catalog.Get(changed.ProviderID)
		if !exists {
			return
		}
		if changed.Up {
			_ = notifications.Raise(
				notificationsdomain.KindProviderHealth, notificationsdomain.SeveritySuccess,
				provider.Name+" is reachable again",
				"The endpoint started answering. Requests no longer avoid it.",
			)
			return
		}
		_ = notifications.Raise(
			notificationsdomain.KindProviderHealth, notificationsdomain.SeverityDanger,
			provider.Name+" is unreachable",
			"The endpoint stopped answering: "+changed.Reason+".",
		)
	})
	// The mode is a setting, so changing it must take effect on the next request
	// rather than at the next launch. The per-provider override table rides the
	// same path: settings own it, the inspector enforces it.
	settingsService.OnApplied(func(applied settingsdomain.Settings) {
		normalized := applied.Normalized()
		if mode, err := guardraildomain.ParseMode(normalized.GuardrailMode); err == nil {
			_ = guardrails.SetMode(mode)
		}
		guardrails.SetProviderModes(normalized.GuardrailProviderModes)
		healthMonitor.SetEnabled(applied.ProviderHealthEnabled)
		// The update cadence rides the same live-apply path: a save in
		// Settings re-arms the refresher without a restart, and turning it
		// back on asks immediately.
		updatesRefresher.SetInterval(updatesapp.IntervalFromSetting(normalized.UpdateCheckInterval))
		routeService.SetChainMode(normalized.ChainMode)
		routes.SetFailoverEnabled(applied.FailoverEnabled)
		// Everything below used to wait for a restart; it applies in place now.
		// The port move rebinds the listener around a stop/start of the relay;
		// the rest just reconfigure their own piece.
		httpRuntime.Reconfigure(relayhttp.Config{
			MaxRequestBytes:       int64(applied.MaxRequestMiB) * 1024 * 1024,
			ResponseHeaderTimeout: time.Duration(applied.HeaderTimeoutSeconds) * time.Second,
			StreamIdleTimeout:     time.Duration(applied.StreamIdleSeconds) * time.Second,
			HeartbeatInterval:     time.Duration(applied.HeartbeatSeconds) * time.Second,
			LiveStreamProbation:   time.Duration(applied.StreamProbationMilliseconds) * time.Millisecond,
			RetryBase:             time.Duration(applied.RetryBaseMilliseconds) * time.Millisecond,
			RetryMax:              time.Duration(applied.RetryMaxSeconds) * time.Second,
			PermanentAttempts:     applied.PermanentAttempts,
		})
		keyScheduler.SetMaxQueued(applied.MaxQueued)
		activity.SetCapacity(applied.ActivityCapacity)
		if store, ok := history.(interface{ SetRetentionDays(int) }); ok {
			store.SetRetentionDays(applied.HistoryRetentionDays)
		}
		guardrails.SetCapacity(normalized.GuardrailFindings)
		modelGateway.SetTimeout(max(time.Duration(applied.HeaderTimeoutSeconds)*time.Second, 90*time.Second))
		next := net.JoinHostPort("127.0.0.1", strconv.Itoa(environmentPort(applied.ListenerPort)))
		if next != httpRuntime.Address() {
			previous := httpRuntime.Address()
			live := relay.Snapshot().State == relaydomain.StateLive
			if live {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = relay.Stop(ctx)
				cancel()
			}
			httpRuntime.SetAddress(next)
			// A relay.stop racing the rebind wins: the address moved, but nobody
			// resurrects a relay the operator just stopped.
			if live && relay.Snapshot().State == relaydomain.StateStopped {
				if _, err := relay.Start(); err != nil {
					// Roll the address back so the recorded settings and the
					// bound listener cannot disagree silently; saving the same
					// port again then retries instead of no-op'ing.
					httpRuntime.SetAddress(previous)
					logger.Printf("relay could not bind the new port; the listener stayed on the old one")
				}
			}
		}
	})
	// The initial values come from the same record, so a restart and a live
	// change leave the relay in the same state.
	routeService.SetChainMode(settings.Normalized().ChainMode)
	routes.SetFailoverEnabled(settings.FailoverEnabled)
	routestdio.Register(protocol, routeService)
	modelstdio.Register(protocol, modelService)
	publishing, err := registerPublishing(protocol, editionDependencies{
		catalog: catalog, keys: keyManager, routes: routeService,
		relay: httpRuntime, settings: settings, applySettings: settingsService.OnApplied, logger: logger,
	})
	if err != nil {
		return nil, err
	}
	return &App{
		protocol:         protocol,
		relay:            relay,
		history:          history,
		analytics:        analyticsFacts,
		publishing:       publishing,
		logger:           logger,
		healthMonitor:    healthMonitor,
		updatesRefresher: updatesRefresher,
		codex:            codexService,
	}, nil
}

func (app *App) Run(ctx context.Context) error {
	if app.healthMonitor != nil && app.healthMonitor.Enabled() {
		healthCtx, cancelHealth := context.WithCancel(context.Background())
		app.healthCancel = cancelHealth
		go app.healthMonitor.Run(healthCtx, 2*time.Minute)
	}
	// The update refresher runs on its own lifetime, not Run's context:
	// like the health monitor it must keep answering between the relay
	// stopping and the process ending, and shutdown cancels it explicitly.
	// A zero interval keeps the goroutine inert — a nil tick channel means
	// "not asking" — so the loop is unconditional and SetInterval can turn
	// it on later without a restart.
	if app.updatesRefresher != nil {
		updatesCtx, cancelUpdates := context.WithCancel(context.Background())
		app.updatesCancel = cancelUpdates
		go app.updatesRefresher.Run(updatesCtx)
	}
	if _, err := app.relay.Start(); err != nil {
		app.logger.Printf("relay start failed")
	}
	// The codex session restores in the background: sign-in state is not a
	// startup dependency, the commands answer immediately, and the session's
	// own change events announce it when it lands. Restore gets Run's
	// context because the refresh loop it starts outlives logins and
	// logouts — only the application's lifetime ends it. A failed restore
	// is logged, never fatal: the app runs with the provider signed out.
	go func() {
		if err := app.codex.Restore(ctx); err != nil {
			app.logger.Printf("codex session could not be restored: %v", err)
		}
	}()
	err := app.protocol.Serve(ctx)
	publishingCtx, cancelPublishing := context.WithTimeout(context.Background(), 5*time.Second)
	if stopErr := app.publishing.Stop(publishingCtx); stopErr != nil {
		app.logger.Printf("the publishing runtime did not stop cleanly")
	}
	cancelPublishing()
	if app.healthCancel != nil {
		app.healthCancel()
	}
	if app.updatesCancel != nil {
		app.updatesCancel()
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
	if app.analytics != nil {
		if err := app.analytics.Close(); err != nil {
			app.logger.Printf("analytics did not close cleanly")
		}
	}
	publishingHistoryCtx, cancelPublishingHistory := context.WithTimeout(context.Background(), 5*time.Second)
	if historyErr := app.publishing.Close(publishingHistoryCtx); historyErr != nil {
		app.logger.Printf("the publishing history did not close cleanly")
	}
	cancelPublishingHistory()
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
	inspector, err := guardrailapp.NewInspector(engine, mode, normalized.GuardrailFindings)
	if err != nil {
		return nil, err
	}
	inspector.SetProviderModes(normalized.GuardrailProviderModes)
	return inspector, nil
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

func defaultKeyManager(providers []providerdomain.Provider, maxQueued int) (*keyapp.Scheduler, *keyapp.Manager, *keydpapi.Repository, error, error) {
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
		return nil, nil, nil, errors.New("key settings path must be absolute"), nil
	}
	repository := keydpapi.New(path)
	manager, err := keyapp.NewManager(scheduler, repository, rates, nil)
	if err != nil {
		return nil, nil, nil, err, nil
	}
	return scheduler, manager, repository, pathErr, nil
}

func defaultProviderManager(catalog *providerapp.Catalog, keys *keyapp.Manager) (*providerapp.Manager, *providerdpapi.Repository, error, error) {
	path := os.Getenv("SWITCHBOARD_PROVIDERS_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = providerdpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, errors.New("provider settings path must be absolute"), nil
	}
	repository := providerdpapi.New(path)
	manager, err := providerapp.NewManager(catalog, repository, keys)
	if err != nil {
		return nil, nil, err, nil
	}
	loadErr := pathErr
	if loadErr == nil {
		loadErr = manager.Load(context.Background())
	}
	return manager, repository, loadErr, nil
}

// defaultCodexStore locates the directory Codex accounts persist to and
// the pre-accounts single-session file that migrates into it, on the
// settings-store convention: an explicit path must be absolute and is a
// hard startup error otherwise, while a machine where the default
// location cannot be resolved still boots — sign-ins then refuse to
// persist instead of writing into the working directory. The explicit
// path names the legacy file, because that is what the variable has
// always named; the account directory is its sibling, so a redirected
// tree stays self-contained.
func defaultCodexStore() (string, string, error, error) {
	path := os.Getenv("SWITCHBOARD_CODEX_PATH")
	if path == "" {
		dir, legacyPath, pathErr := codexstore.DefaultPaths()
		return dir, legacyPath, pathErr, nil
	}
	if !filepath.IsAbs(path) {
		return "", "", nil, errors.New("codex account path must be absolute")
	}
	return filepath.Join(filepath.Dir(path), "codex-accounts"), path, nil, nil
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

func defaultHistory(retentionDays int, onDrop func(cause string)) (activityapp.History, string, error) {
	path := os.Getenv("SWITCHBOARD_HISTORY_PATH")
	var err error
	if path == "" {
		path, err = activitysqlite.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, "", errors.New("history path must be absolute")
	}
	if err != nil {
		return nil, "", err
	}
	// Registered before the store can be handed to anything that records, so
	// no drop can happen unwitnessed.
	store, err := activitysqlite.Open(path, retentionDays)
	if err != nil {
		return nil, path, err
	}
	// Registered before the store can be handed to anything that records, so
	// no drop can happen unwitnessed.
	store.OnDrop(onDrop)
	return store, path, nil
}

// repairMemoPath places the relay's learned-shape file beside the history
// database: same volume, same backup story, no secret in it either way.
// providerNames adapts the provider catalog to the analytics name port:
// reports read the current display name of each id, so a renamed provider does
// not keep wearing the old name in every future report.
type providerNames struct {
	catalog *providerapp.Catalog
}

func (source providerNames) Names(context.Context) (map[string]string, error) {
	names := make(map[string]string)
	for _, provider := range source.catalog.List() {
		names[provider.ID] = provider.Name
	}
	return names, nil
}

func repairMemoPath(historyPath string) string {
	if historyPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(historyPath), "repair-memo.json")
}

func defaultRouteService(catalog *providerapp.Catalog) (*routeapp.Service, *routedpapi.Repository, error, error) {
	path := os.Getenv("SWITCHBOARD_ROUTES_PATH")
	var pathErr error
	if path == "" {
		path, pathErr = routedpapi.DefaultPath()
	} else if !filepath.IsAbs(path) {
		return nil, nil, errors.New("routes path must be absolute"), nil
	}
	repository := routedpapi.New(path)
	service, err := routeapp.NewService(repository, routeproviders.NewCatalog(catalog))
	if err != nil {
		return nil, nil, err, nil
	}
	loadErr := pathErr
	if loadErr == nil {
		loadErr = service.Load(context.Background())
	}
	return service, repository, loadErr, nil
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
