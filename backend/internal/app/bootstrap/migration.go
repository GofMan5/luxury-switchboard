package bootstrap

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"

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
	if providerErr != nil || keyErr != nil || routeErr != nil || tunnelErr != nil {
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
	// The publish is per-file (importEachMissing below), and deliberately so.
	// An all-or-nothing rename sequence left a crash between renames
	// half-done forever: this gate used to answer "a destination exists" with
	// "never migrate again", so the files a crash HAD landed barred the ones
	// it never wrote from ever arriving — the operator kept half a migration
	// with no way to finish it. importEachMissing skips what already landed
	// and goes silent when nothing is missing, so a fully migrated install
	// stays a no-op; a half-migrated one completes.
	destinations := []migrationDestination{
		{providerPath, func(temp string) error {
			return providerdpapi.New(temp).Save(ctx, providerapp.SavedState{Providers: legacy.Providers, ActiveID: legacy.ActiveID})
		}},
		{keyPath, func(temp string) error { return keydpapi.New(temp).Save(ctx, legacy.Keys) }},
		{routePath, func(temp string) error { return routedpapi.New(temp).Save(ctx, legacy.Routes) }},
	}
	if legacy.HasTunnel {
		destinations = append(destinations, migrationDestination{tunnelPath, func(temp string) error {
			return tunneldpapi.New(temp).Save(ctx, legacy.Tunnel)
		}})
	}
	importEachMissing(logger, destinations)
}

// migrationDestination is one file the legacy import still owes: where it must
// land and how to write its staged copy.
type migrationDestination struct {
	final string
	save  func(temp string) error
}

// importEachMissing publishes every destination that has not landed yet, one
// file at a time. A failure costs exactly the file it happened on: the run
// logs it and moves to the next, because the previous all-or-nothing sequence
// is what stranded half a migration forever — anyExists then saw the files
// that HAD landed and refused to run again, so the ones the same crash never
// wrote were never coming.
func importEachMissing(logger *log.Logger, destinations []migrationDestination) {
	imported, attempted := 0, 0
	for _, destination := range destinations {
		if _, err := os.Stat(destination.final); err == nil || !errors.Is(err, os.ErrNotExist) {
			continue
		}
		attempted++
		temp := destination.final + ".migration-" + strconv.Itoa(os.Getpid())
		if err := destination.save(temp); err != nil {
			_ = os.Remove(temp)
			logger.Printf("legacy settings import was incomplete")
			continue
		}
		if err := os.Rename(temp, destination.final); err != nil {
			_ = os.Remove(temp)
			logger.Printf("legacy settings import was incomplete")
			continue
		}
		imported++
	}
	if attempted == 0 {
		return
	}
	if imported == attempted {
		logger.Printf("legacy settings imported into encrypted Go storage")
	} else {
		logger.Printf("legacy settings partially imported; the rest retries on the next launch")
	}
}
