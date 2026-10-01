package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

type profileStore struct {
	saved    map[string]domain.Profile
	removed  []string
	recorded int
	failing  bool
}

func newProfileStore() *profileStore {
	return &profileStore{saved: map[string]domain.Profile{}}
}

func (store *profileStore) Record(domain.Event) bool { store.recorded++; return true }
func (store *profileStore) Recent(context.Context, string, int) ([]domain.Event, error) {
	return nil, nil
}
func (store *profileStore) Close(context.Context) error { return nil }
func (store *profileStore) Profiles(context.Context) ([]domain.Profile, error) {
	result := make([]domain.Profile, 0, len(store.saved))
	for _, profile := range store.saved {
		result = append(result, profile)
	}
	return result, nil
}
func (store *profileStore) SaveProfile(_ context.Context, profile domain.Profile) error {
	if store.failing {
		return errors.New("store unavailable")
	}
	store.saved[profile.IP] = profile
	return nil
}
func (store *profileStore) DeleteProfile(_ context.Context, ip string) error {
	delete(store.saved, ip)
	store.removed = append(store.removed, ip)
	return nil
}

func TestBansAndNotesSurviveRestartAndStayVisibleWhenIdle(t *testing.T) {
	store := newProfileStore()
	service := NewService(store)
	if err := service.SetProfile(context.Background(), domain.Profile{IP: "203.0.113.7", Banned: true, Note: " abusive client "}); err != nil {
		t.Fatal(err)
	}
	if !service.Banned("203.0.113.7") || service.Banned("198.51.100.4") {
		t.Fatal("ban was not applied to exactly one address")
	}

	restarted := NewService(store)
	if err := restarted.LoadProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !restarted.Banned("203.0.113.7") {
		t.Fatal("ban did not survive a restart")
	}
	clients := restarted.List()
	if len(clients) != 1 || clients[0].IP != "203.0.113.7" || !clients[0].Banned || clients[0].Note != "abusive client" {
		t.Fatalf("owner decisions are not visible for an idle client: %+v", clients)
	}
}

// An unreadable stored row costs exactly itself: the readable bans still come
// back into force, and the count of what could not be read is reported — a
// dropped ban must read as "could not be read", not as "never existed".
func TestAnUnreadableStoredRowIsReportedButCostsOnlyItself(t *testing.T) {
	store := newProfileStore()
	service := NewService(store)
	if err := service.SetProfile(context.Background(), domain.Profile{IP: "203.0.113.7", Banned: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetProfile(context.Background(), domain.Profile{IP: "198.51.100.4", Banned: true}); err != nil {
		t.Fatal(err)
	}
	// A row the canonicalizer refuses — an address no gateway would report.
	store.saved["garbage"] = domain.Profile{IP: "not an address at all", Banned: true}

	restarted := NewService(store)
	err := restarted.LoadProfiles(context.Background())
	if err == nil {
		t.Fatal("an unreadable stored row was read as a clean restore")
	}
	if !strings.Contains(err.Error(), "1") {
		t.Fatalf("the report does not name the number of unreadable rows: %v", err)
	}
	if !restarted.Banned("203.0.113.7") || !restarted.Banned("198.51.100.4") {
		t.Fatal("the failure of one row cost the readable bans their force")
	}
	if restarted.Banned("not an address at all") {
		t.Fatal("an uncanonicalizable row became an enforced ban")
	}
}

func TestProfileAddressIsStoredExactlyAsTheGatewayReportsIt(t *testing.T) {
	store := newProfileStore()
	service := NewService(store)
	cases := map[string]string{
		"2001:DB8::1":        "2001:db8::1",
		"::ffff:203.0.113.7": "203.0.113.7",
		" 198.51.100.4 ":     "198.51.100.4",
	}
	for entered, canonical := range cases {
		if err := service.SetProfile(context.Background(), domain.Profile{IP: entered, Banned: true}); err != nil {
			t.Fatalf("%q was rejected: %v", entered, err)
		}
		if !service.Banned(canonical) {
			t.Fatalf("a ban entered as %q never applies to %q", entered, canonical)
		}
	}
}

func TestClearedProfileIsForgotten(t *testing.T) {
	store := newProfileStore()
	service := NewService(store)
	ctx := context.Background()
	if err := service.SetProfile(ctx, domain.Profile{IP: "203.0.113.7", Banned: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.SetProfile(ctx, domain.Profile{IP: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
	if service.Banned("203.0.113.7") || len(service.List()) != 0 || len(store.saved) != 0 {
		t.Fatal("clearing a profile left state behind")
	}
	if len(store.removed) != 1 {
		t.Fatalf("cleared profile was not deleted from storage: %v", store.removed)
	}
}

func TestInvalidProfilesAreRejectedAndStorageFailuresDoNotApply(t *testing.T) {
	store := newProfileStore()
	service := NewService(store)
	for _, profile := range []domain.Profile{
		{IP: "not-an-ip", Banned: true},
		{IP: "203.0.113.7", Note: strings.Repeat("n", 501)},
		{IP: "203.0.113.7", Note: "line\r\nbreak"},
		{IP: "203.0.113.7", Note: "ansi\x1b[31mred"},
		{IP: "203.0.113.7", Note: "spoof‮gnitirw"},
	} {
		if service.SetProfile(context.Background(), profile) == nil {
			t.Fatalf("invalid profile was accepted: %q", profile.Note)
		}
	}
	store.failing = true
	if service.SetProfile(context.Background(), domain.Profile{IP: "203.0.113.7", Banned: true}) == nil {
		t.Fatal("a storage failure was reported as success")
	}
	if service.Banned("203.0.113.7") {
		t.Fatal("a ban applied in memory without being persisted")
	}
}

// Same rule, the other way to have no storage. This one used to be the exception:
// the ban applied in memory, the list showed it in force, and the next launch
// dropped it - so the tunnel served an address the owner had banned, with nothing
// on screen having said otherwise.
func TestWithoutStorageABanIsRefusedRatherThanKeptInMemory(t *testing.T) {
	service := NewService(nil)
	if service.SetProfile(context.Background(), domain.Profile{IP: "203.0.113.7", Banned: true}) == nil {
		t.Fatal("a ban was accepted with nowhere to store it")
	}
	if service.Banned("203.0.113.7") || len(service.List()) != 0 {
		t.Fatal("a ban applied in memory with nowhere to store it")
	}
	// The decisions of every earlier session are unreachable, which is not the same
	// as there having been none, so the caller has to be able to tell.
	if service.LoadProfiles(context.Background()) == nil {
		t.Fatal("unreachable profiles were reported as an empty set")
	}
}

func TestClientListOrderIsStableAcrossRefreshes(t *testing.T) {
	service := NewService(newProfileStore())
	for _, ip := range []string{"10.0.0.5", "10.0.0.1", "10.0.0.4", "10.0.0.2", "10.0.0.3", "10.0.0.6"} {
		if err := service.SetProfile(context.Background(), domain.Profile{IP: ip, Banned: true}); err != nil {
			t.Fatal(err)
		}
	}
	first := addresses(service.List())
	if first != "10.0.0.1 10.0.0.2 10.0.0.3 10.0.0.4 10.0.0.5 10.0.0.6" {
		t.Fatalf("idle profiles are not ordered by address: %s", first)
	}
	for range 30 {
		if again := addresses(service.List()); again != first {
			t.Fatalf("the client list reshuffles between refreshes:\n%s\n%s", first, again)
		}
	}
}

func TestRefusedAttemptsStayCheaperThanServedRequests(t *testing.T) {
	store := newProfileStore()
	service := NewService(store)
	updates := 0
	service.OnChanged(func() { updates++ })
	if err := service.SetProfile(context.Background(), domain.Profile{IP: "203.0.113.7", Banned: true}); err != nil {
		t.Fatal(err)
	}
	for range 5_000 {
		service.Reject("203.0.113.7")
	}
	clients := service.List()
	if len(clients) != 1 || clients[0].Refused != 5_000 {
		t.Fatalf("refused attempts were not counted: %+v", clients)
	}
	if clients[0].ActualRPM != 0 || clients[0].Count != 0 {
		t.Fatalf("a refusal was charged as a served request: %+v", clients[0])
	}
	if len(service.Events(context.Background(), "203.0.113.7")) != 0 || store.recorded != 0 {
		t.Fatalf("refusals wrote request history: recorded=%d", store.recorded)
	}
	if updates > 6 {
		t.Fatalf("every refusal published its own update: %d", updates)
	}
}

func TestRefusalsFromUndecidedAddressesTrackNothing(t *testing.T) {
	service := NewService(newProfileStore())
	if err := service.SetProfile(context.Background(), domain.Profile{IP: "203.0.113.7", Banned: true}); err != nil {
		t.Fatal(err)
	}
	for index := range 20_000 {
		service.Reject(fmt.Sprintf("198.51.%d.%d", index/256, index%256))
	}
	clients := service.List()
	if len(clients) != 1 || clients[0].IP != "203.0.113.7" {
		t.Fatalf("addresses the owner never decided about became tracked clients: %d entries", len(clients))
	}
}

func addresses(clients []domain.Client) string {
	values := make([]string, 0, len(clients))
	for _, client := range clients {
		values = append(values, client.IP)
	}
	return strings.Join(values, " ")
}
