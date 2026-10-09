package application

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

type memoryRepository struct {
	values  []domain.Assignment
	loadErr error
	fail    bool
}

func (repository *memoryRepository) Load(context.Context) ([]domain.Assignment, error) {
	if repository.loadErr != nil {
		return nil, repository.loadErr
	}
	return slices.Clone(repository.values), nil
}

func TestFailedLoadBlocksFallbackAndOverwrite(t *testing.T) {
	loadErr := errors.New("encrypted route store unavailable")
	repository := &memoryRepository{loadErr: loadErr}
	service, _ := NewService(repository, providers{"echo": true})
	if err := service.Load(context.Background()); !errors.Is(err, loadErr) {
		t.Fatalf("load failure was hidden: %v", err)
	}
	if _, ok := service.Resolve(domain.TargetRelay, "public"); ok {
		t.Fatal("route resolved after its durable state failed to load")
	}
	err := service.Upsert(context.Background(), domain.Assignment{Target: domain.TargetRelay, PublicModel: "public", UpstreamModel: "private", ProviderID: "echo", Enabled: true})
	if !errors.Is(err, loadErr) {
		t.Fatalf("unreadable route store could be overwritten: %v", err)
	}
}
func (repository *memoryRepository) Save(_ context.Context, values []domain.Assignment) error {
	if repository.fail {
		return errors.New("injected")
	}
	repository.values = slices.Clone(values)
	return nil
}

type providers map[string]bool

func (value providers) Exists(id string) bool { return value[id] }
func TestAliasesResolveToTheSameUpstreamModel(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"echo": true})
	route := domain.Assignment{Target: domain.TargetRelay, PublicModel: "claude-opus-5", UpstreamModel: "claude-5-opus", ProviderID: "echo", Aliases: []string{"claude-opus-5[1m]"}, Enabled: true}
	if err := service.Upsert(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{"claude-opus-5", "claude-opus-5[1m]"} {
		got, ok := service.Resolve(domain.TargetRelay, requested)
		if !ok || got.UpstreamModel != "claude-5-opus" {
			t.Fatalf("alias %q did not resolve: %+v", requested, got)
		}
	}
	if _, ok := service.Resolve(domain.TargetRelay, "claude-5-opus"); ok {
		t.Fatal("an unrelated name resolved through the alias")
	}
}

func TestAliasCollisionIsRejected(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"echo": true, "custom": true})
	first := domain.Assignment{Target: domain.TargetRelay, PublicModel: "model-a", UpstreamModel: "upstream-a", ProviderID: "echo", Enabled: true}
	if err := service.Upsert(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := domain.Assignment{Target: domain.TargetRelay, PublicModel: "model-b", UpstreamModel: "upstream-b", ProviderID: "custom", Aliases: []string{"model-a"}, Enabled: true}
	if err := service.Upsert(context.Background(), second); err == nil {
		t.Fatal("an alias shadowing another route was accepted")
	}
	if got, ok := service.Resolve(domain.TargetRelay, "model-a"); !ok || got.ProviderID != "echo" {
		t.Fatalf("the original route was disturbed by a rejected alias: %+v", got)
	}
}

func TestAliasCollisionFailsLoad(t *testing.T) {
	repository := &memoryRepository{values: []domain.Assignment{
		{Target: domain.TargetRelay, PublicModel: "model-a", UpstreamModel: "upstream-a", ProviderID: "echo", Enabled: true},
		{Target: domain.TargetRelay, PublicModel: "model-b", UpstreamModel: "upstream-b", ProviderID: "custom", Aliases: []string{"model-a"}, Enabled: true},
	}}
	service, _ := NewService(repository, providers{"echo": true, "custom": true})
	if err := service.Load(context.Background()); err == nil {
		t.Fatal("routes with colliding aliases loaded cleanly")
	}
	if _, ok := service.Resolve(domain.TargetRelay, "model-a"); ok {
		t.Fatal("routes resolved after an invalid load")
	}
}

func TestRelayAndTunnelRoutesRemainIndependent(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"echo": true, "custom": true})
	relay := domain.Assignment{Target: domain.TargetRelay, PublicModel: "gpt-x", UpstreamModel: "gpt-x", ProviderID: "echo", Enabled: true}
	tunnel := domain.Assignment{Target: domain.TargetTunnel, PublicModel: "public-gpt", UpstreamModel: "gpt-x", ProviderID: "custom", ContextLimitKiB: 128 * 1024, Enabled: true}
	if err := service.Upsert(context.Background(), relay); err != nil {
		t.Fatal(err)
	}
	if err := service.Upsert(context.Background(), tunnel); err != nil {
		t.Fatal(err)
	}
	if got, ok := service.Resolve(domain.TargetRelay, "gpt-x"); !ok || got.ProviderID != "echo" {
		t.Fatalf("relay route missing: %+v", got)
	}
	if got, ok := service.Resolve(domain.TargetTunnel, "public-gpt"); !ok || got.UpstreamModel != "gpt-x" {
		t.Fatalf("tunnel route missing: %+v", got)
	}
	if err := service.Delete(context.Background(), domain.TargetRelay, "gpt-x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Resolve(domain.TargetTunnel, "public-gpt"); !ok {
		t.Fatal("relay deletion changed tunnel route")
	}
}

// The same public name on relay and tunnel is normal: the tunnel republishes a
// model the relay already serves. Only a name claimed twice on one target is
// ambiguous, so the collision check must be keyed by target.
func TestSameNameOnRelayAndTunnelIsAllowed(t *testing.T) {
	routes := []domain.Assignment{
		{Target: domain.TargetRelay, PublicModel: "claude-opus-5", UpstreamModel: "upstream-a", ProviderID: "echo", Aliases: []string{"opus"}, Enabled: true},
		{Target: domain.TargetTunnel, PublicModel: "opus", UpstreamModel: "upstream-b", ProviderID: "custom", Aliases: []string{"claude-opus-5"}, ContextLimitKiB: 128 * 1024, Enabled: true},
	}
	repository := &memoryRepository{values: routes}
	service, _ := NewService(repository, providers{"echo": true, "custom": true})
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("a name shared across targets must load: %v", err)
	}
	if got, ok := service.Resolve(domain.TargetRelay, "opus"); !ok || got.UpstreamModel != "upstream-a" {
		t.Fatalf("relay alias resolves to the wrong route: %+v", got)
	}
	if got, ok := service.Resolve(domain.TargetTunnel, "claude-opus-5"); !ok || got.UpstreamModel != "upstream-b" {
		t.Fatalf("tunnel alias resolves to the wrong route: %+v", got)
	}
}

func TestFailedSaveDoesNotMutateRoutes(t *testing.T) {
	repository := &memoryRepository{fail: true}
	service, _ := NewService(repository, providers{"echo": true})
	err := service.Upsert(context.Background(), domain.Assignment{Target: domain.TargetRelay, PublicModel: "gpt", UpstreamModel: "gpt", ProviderID: "echo", Enabled: true})
	if err == nil {
		t.Fatal("save failure ignored")
	}
	if len(service.List(domain.TargetRelay)) != 0 {
		t.Fatal("failed save mutated routes")
	}
}

func TestBulkUpsertPersistsRoutesOnce(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"echo": true})
	routes := []domain.Assignment{
		{Target: domain.TargetTunnel, PublicModel: "public-a", UpstreamModel: "model-a", ProviderID: "echo", Enabled: true},
		{Target: domain.TargetTunnel, PublicModel: "public-b", UpstreamModel: "model-b", ProviderID: "echo", Enabled: true},
	}
	if err := service.UpsertMany(context.Background(), routes); err != nil {
		t.Fatal(err)
	}
	if len(service.List(domain.TargetTunnel)) != 2 || len(repository.values) != 2 {
		t.Fatal("bulk routes were not persisted")
	}
}

func TestLoadKeepsStaleProviderRouteVisibleAndFailClosed(t *testing.T) {
	stale := domain.Assignment{Target: domain.TargetRelay, PublicModel: "public", UpstreamModel: "private", ProviderID: "removed", Enabled: true}
	service, _ := NewService(&memoryRepository{values: []domain.Assignment{stale}}, providers{"active": true})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if routes := service.List(domain.TargetRelay); len(routes) != 1 || !reflect.DeepEqual(routes[0], stale) {
		t.Fatalf("stale route was silently discarded: %+v", routes)
	}
	if err := service.Upsert(context.Background(), stale); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("stale provider became writable again: %v", err)
	}
}

func TestDuplicatePersistedAndBulkRoutesAreRejected(t *testing.T) {
	route := domain.Assignment{Target: domain.TargetTunnel, PublicModel: "public", UpstreamModel: "private", ProviderID: "echo", Enabled: true}
	service, _ := NewService(&memoryRepository{values: []domain.Assignment{route, route}}, providers{"echo": true})
	if err := service.Load(context.Background()); err == nil {
		t.Fatal("duplicate persisted routes were accepted")
	}
	service, _ = NewService(&memoryRepository{}, providers{"echo": true})
	if err := service.UpsertMany(context.Background(), []domain.Assignment{route, route}); err == nil {
		t.Fatal("duplicate route batch was accepted")
	}
	if err := service.Delete(context.Background(), domain.Target("invalid"), "public"); err == nil {
		t.Fatal("invalid route deletion was reported successful")
	}
}

// Balance mode round-robins the healthy entries of a chain, so two providers
// alternate — the shared daily quota of each is spent half as fast — while a
// degraded one sits out the rotation and an all-degraded chain still serves.
func TestBalanceModeRoundRobinsHealthyChainEntries(t *testing.T) {
	clock := time.Unix(0, 0)
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"alpha-relay": true, "agent": true, "third": true})
	service.degradeClock = func() time.Time { return clock }
	for _, entry := range []struct {
		provider string
		priority int
	}{{"alpha-relay", 0}, {"agent", 1}, {"third", 2}} {
		if err := service.Upsert(context.Background(), domain.Assignment{
			Target: domain.TargetRelay, PublicModel: "glm", UpstreamModel: "glm-" + entry.provider,
			ProviderID: entry.provider, Priority: entry.priority, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	service.SetChainMode("balance")
	seen := map[string]int{}
	for range 6 {
		got, ok := service.Resolve(domain.TargetRelay, "glm")
		if !ok {
			t.Fatal("the balanced chain refused to serve")
		}
		seen[got.ProviderID]++
	}
	for provider, count := range seen {
		if count != 2 {
			t.Fatalf("the rotation was not fair: %s served %d of 6", provider, count)
		}
	}
	// A degraded entry sits out the rotation; the rest keep sharing.
	service.Degrade("alpha-relay")
	seen = map[string]int{}
	for range 6 {
		got, _ := service.Resolve(domain.TargetRelay, "glm")
		seen[got.ProviderID]++
	}
	if seen["alpha-relay"] != 0 || seen["agent"] != 3 || seen["third"] != 3 {
		t.Fatalf("a degraded entry kept taking traffic: %+v", seen)
	}
	// Everything degraded: the chain still serves, by priority.
	service.Degrade("agent")
	service.Degrade("third")
	got, ok := service.Resolve(domain.TargetRelay, "glm")
	if !ok || got.ProviderID != "alpha-relay" {
		t.Fatalf("an all-degraded chain refused to serve: %+v ok=%v", got, ok)
	}
	// The tunnel target ignores the mode: one route per model, always.
	if got, _ := service.Resolve(domain.TargetTunnel, "glm"); got.ProviderID != "" {
		t.Fatalf("the tunnel target participated in the rotation: %+v", got)
	}
	// Strict mode returns to priority order (all-degraded degrades to the
	// configured order rather than refusing).
	service.SetChainMode("failover")
	if got, _ := service.Resolve(domain.TargetRelay, "glm"); got.ProviderID != "alpha-relay" {
		t.Fatalf("strict mode did not follow priority: %+v", got)
	}
}

// One provider entry needs no rotation: balance mode must not become a
// coin flip that occasionally answers nothing.
func TestBalanceModeWithASingleEntryAlwaysServes(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"solo": true})
	if err := service.Upsert(context.Background(), domain.Assignment{
		Target: domain.TargetRelay, PublicModel: "glm", UpstreamModel: "glm-solo", ProviderID: "solo", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	service.SetChainMode("balance")
	for range 3 {
		if got, ok := service.Resolve(domain.TargetRelay, "glm"); !ok || got.ProviderID != "solo" {
			t.Fatalf("a single-entry chain missed: %+v ok=%v", got, ok)
		}
	}
}

// The relay target is an ordered chain: siblings whose provider is degraded
// come later even at equal priority, and an all-degraded chain still serves.
func TestFailoverChainPrefersSiblingsOfADegradedProvider(t *testing.T) {
	clock := time.Unix(0, 0)
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"alpha-relay": true, "agent": true})
	service.degradeClock = func() time.Time { return clock }
	if err := service.Upsert(context.Background(), domain.Assignment{Target: domain.TargetRelay, PublicModel: "glm", UpstreamModel: "glm-5.3", ProviderID: "alpha-relay", Priority: 0, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.Upsert(context.Background(), domain.Assignment{Target: domain.TargetRelay, PublicModel: "glm", UpstreamModel: "glm-5.3", ProviderID: "agent", Priority: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := service.Resolve(domain.TargetRelay, "glm"); got.ProviderID != "alpha-relay" {
		t.Fatalf("the chain did not start at its priority head: %+v", got)
	}
	chain := service.Chain("glm")
	if len(chain) != 2 || chain[0].ProviderID != "alpha-relay" || chain[1].ProviderID != "agent" {
		t.Fatalf("the chain was not ordered: %+v", chain)
	}
	// The primary answers a terminal verdict: its sibling moves up for the
	// TTL, and the chain still serves rather than answering "no route".
	service.Degrade("alpha-relay")
	if got, _ := service.Resolve(domain.TargetRelay, "glm"); got.ProviderID != "agent" {
		t.Fatalf("a degraded head still took the request: %+v", got)
	}
	// Both degraded is preference, not removal: the chain falls back to its
	// configured order rather than refusing.
	service.Degrade("agent")
	if got, ok := service.Resolve(domain.TargetRelay, "glm"); !ok || got.ProviderID != "alpha-relay" {
		t.Fatalf("an all-degraded chain refused to serve: %+v ok=%v", got, ok)
	}
	// The TTL lapses and the primary takes over again.
	clock = clock.Add(degradeTTL + time.Second)
	if got, _ := service.Resolve(domain.TargetRelay, "glm"); got.ProviderID != "alpha-relay" {
		t.Fatalf("the degraded provider was never re-probed: %+v", got)
	}
}

// Upserting the same model on a second provider adds a chain entry rather than
// replacing the first, and deleting the model removes the whole chain.
func TestRelayRowsAreKeyedByModelAndProvider(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"alpha-relay": true, "agent": true})
	for _, provider := range []string{"alpha-relay", "agent"} {
		if err := service.Upsert(context.Background(), domain.Assignment{Target: domain.TargetRelay, PublicModel: "glm", UpstreamModel: "glm-5.3", ProviderID: provider, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if len(service.List(domain.TargetRelay)) != 2 {
		t.Fatalf("the second provider replaced the first: %+v", service.List(domain.TargetRelay))
	}
	if err := service.Delete(context.Background(), domain.TargetRelay, "glm"); err != nil {
		t.Fatal(err)
	}
	if len(service.List(domain.TargetRelay)) != 0 {
		t.Fatal("deleting the model left chain rows behind")
	}
}

// The tunnel target publishes one route per public model: a second entry for
// one name would make the public boundary ambiguous.
func TestTunnelTargetRejectsASecondRowPerModel(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"alpha-relay": true, "agent": true})
	first := domain.Assignment{Target: domain.TargetTunnel, PublicModel: "public", UpstreamModel: "up", ProviderID: "alpha-relay", Enabled: true}
	if err := service.Upsert(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := domain.Assignment{Target: domain.TargetTunnel, PublicModel: "public", UpstreamModel: "up", ProviderID: "agent", Enabled: true}
	if err := service.Upsert(context.Background(), second); err != nil {
		t.Fatalf("a second tunnel row was rejected: %v", err)
	}
	if got, _ := service.Resolve(domain.TargetTunnel, "public"); got.ProviderID != "agent" {
		t.Fatalf("the tunnel row was not replaced: %+v", got)
	}
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("the replaced row must load: %v", err)
	}
}

// Deleting a provider is a deliberate cascade: every assignment that names
// it, relay and tunnel alike, goes with the provider, because a failover
// chain that keeps a hole the operator did not ask for is a route that
// silently behaves differently from the one configured. Delete stays the
// operator's single-model tool; this is the hand-off the providers manager
// calls while the provider entry still exists.
func TestRemoveProviderDeletesEveryAssignmentThatNamesIt(t *testing.T) {
	repository := &memoryRepository{values: []domain.Assignment{
		{Target: domain.TargetRelay, PublicModel: "model-a", UpstreamModel: "up-a", ProviderID: "echo", Enabled: true},
		{Target: domain.TargetRelay, PublicModel: "model-b", UpstreamModel: "up-b", ProviderID: "echo", Enabled: true},
		{Target: domain.TargetRelay, PublicModel: "model-b", UpstreamModel: "up-b", ProviderID: "agent", Enabled: true},
		{Target: domain.TargetTunnel, PublicModel: "public-b", UpstreamModel: "up-b", ProviderID: "echo", ContextLimitKiB: 128 * 1024, Enabled: true},
	}}
	service, _ := NewService(repository, providers{"echo": true, "agent": true})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	var notified []domain.Target
	service.OnChanged(func(target domain.Target) { notified = append(notified, target) })

	if err := service.RemoveProvider(context.Background(), "echo"); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Resolve(domain.TargetRelay, "model-a"); ok {
		t.Fatal("a relay route of the removed provider still resolves")
	}
	if got, ok := service.Resolve(domain.TargetRelay, "model-b"); !ok || got.ProviderID != "agent" {
		t.Fatalf("the failover chain lost its surviving provider: %+v", got)
	}
	if _, ok := service.Resolve(domain.TargetTunnel, "public-b"); ok {
		t.Fatal("a tunnel route of the removed provider still resolves")
	}
	for _, assignment := range repository.values {
		if assignment.ProviderID == "echo" {
			t.Fatalf("an assignment of the removed provider was persisted: %+v", assignment)
		}
	}
	if len(repository.values) != 1 {
		t.Fatalf("the surviving assignment must be the only one persisted: %+v", repository.values)
	}
	relay, tunnel := false, false
	for _, target := range notified {
		relay = relay || target == domain.TargetRelay
		tunnel = tunnel || target == domain.TargetTunnel
	}
	if !relay || !tunnel {
		t.Fatalf("listeners were not told about both affected targets: %v", notified)
	}
}
