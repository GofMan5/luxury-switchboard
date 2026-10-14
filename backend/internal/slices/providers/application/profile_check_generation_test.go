package application_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	keyprobe "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/adapters/probe"
	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

type fixtureProfileKeys struct{ keys []keydomain.Key }

func (r *fixtureProfileKeys) Load(context.Context) ([]keydomain.Key, error) { return r.keys, nil }
func (r *fixtureProfileKeys) Save(_ context.Context, keys []keydomain.Key) error {
	r.keys = keys
	return nil
}

type fixtureProfiles struct {
	state providerapp.SavedState
	fail  bool
}

func (r *fixtureProfiles) Load(context.Context) (providerapp.SavedState, error) { return r.state, nil }
func (r *fixtureProfiles) Save(_ context.Context, state providerapp.SavedState) error {
	if r.fail {
		return errors.New("fixture save failure")
	}
	r.state = state
	return nil
}

func TestOldCatalogVerdictCannotPenalizeEditedProviderProfile(t *testing.T) {
	for _, change := range []string{"endpoint", "auth", "catalog", "metadata", "failed-save"} {
		t.Run(change, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32
			old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
					w.WriteHeader(401)
					return
				}
				w.WriteHeader(200)
			}))
			defer old.Close()
			next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
			defer next.Close()
			params := domain.Params{ID: "fixture", Name: "Fixture", BaseURL: old.URL, AuthMode: domain.AuthBearer, ModelsPath: "/models", RPM: 0, Enabled: true}
			provider, err := domain.New(params)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := providerapp.NewCatalog([]domain.Provider{provider}, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			scheduler := keyapp.NewScheduler(10)
			keys, err := keyapp.NewManager(scheduler, &fixtureProfileKeys{}, map[string]keyapp.Rate{"fixture": {}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = keys.Add(context.Background(), keydomain.Params{ProviderID: "fixture", Label: "fixture", Secret: "synthetic"}); err != nil {
				t.Fatal(err)
			}
			profiles := &fixtureProfiles{state: providerapp.SavedState{Providers: []domain.Provider{provider}, ActiveID: "fixture"}}
			manager, err := providerapp.NewManager(catalog, profiles, keys)
			if err != nil {
				t.Fatal(err)
			}
			lease, _, err := scheduler.Acquire(context.Background(), "fixture", "model")
			if err != nil {
				t.Fatal(err)
			}
			lease.Finish(keydomain.Outcome{Kind: keydomain.OutcomeSuccess})
			prober := keyprobe.NewProber(catalog)
			done := make(chan error, 1)
			go func() { _, err := keys.CheckPool(context.Background(), "fixture", prober); done <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("old check did not start")
			}
			switch change {
			case "endpoint":
				params.BaseURL = next.URL
			case "auth":
				params.AuthMode = domain.AuthAPIKey
			case "catalog":
				params.ModelsPath = "/new-models"
			case "metadata":
				params.Name = "Renamed"
				params.RPM = 9
			case "failed-save":
				params.BaseURL = next.URL
				profiles.fail = true
			}
			_, updateErr := manager.Update(context.Background(), "fixture", params)
			if (change == "failed-save") != (updateErr != nil) {
				close(release)
				t.Fatal(updateErr)
			}
			if _, err := keys.CheckPool(context.Background(), "fixture", prober); err != nil {
				close(release)
				t.Fatal(err)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			state := keys.List("fixture")[0]
			wantAuth := 0
			if change == "metadata" || change == "failed-save" {
				wantAuth = 1
			}
			if state.AuthStreak != wantAuth || state.StartsInWindow != 1 {
				t.Fatalf("old catalog verdict crossed profile epoch or quota reset: change=%s row=%+v wantAuth=%d", change, state, wantAuth)
			}
		})
	}
}
