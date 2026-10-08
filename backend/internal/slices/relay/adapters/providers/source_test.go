package providers

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type fixedResolver struct {
	route relayapp.ModelRoute
	err   error
}

func (resolver fixedResolver) Relay(string) (relayapp.ModelRoute, bool, error) {
	return resolver.route, true, resolver.err
}

// Next walks a failover chain past the provider that refused, skipping
// siblings that no longer exist or are disabled, and reports a miss when the
// chain has nothing left worth trying.
func TestNextWalksTheChainPastMissingAndDisabledSiblings(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "active", Name: "Active", BaseURL: "https://active.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	healthy, _ := providerdomain.New(providerdomain.Params{ID: "healthy", Name: "Healthy", BaseURL: "https://healthy.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	disabled, _ := providerdomain.New(providerdomain.Params{ID: "disabled", Name: "Disabled", BaseURL: "https://disabled.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: false})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active, healthy, disabled}, active.ID)
	chain := &fixedChain{
		routes: []relayapp.ModelRoute{
			{ProviderID: "active", UpstreamModel: "up-active"},
			{ProviderID: "gone", UpstreamModel: "up-gone"},
			{ProviderID: "disabled", UpstreamModel: "up-disabled"},
			{ProviderID: "healthy", UpstreamModel: "up-healthy"},
		},
	}
	source := NewSource(catalog, fixedResolver{}, chain)
	next, ok, err := source.Next(context.Background(), "active", "public")
	if err != nil || !ok || next.ProviderID != "healthy" || next.UpstreamModel != "up-healthy" {
		t.Fatalf("the chain did not land on the healthy sibling: ok=%v next=%+v err=%v", ok, next, err)
	}
	// Degrading is delegated to the routes half, so the relay never learns the
	// TTL or the ordering rules.
	source.Degrade("active")
	if len(chain.degraded) != 1 || chain.degraded[0] != "active" {
		t.Fatalf("degradation was not delegated: %v", chain.degraded)
	}
	// The chain exhausted of usable siblings is a miss, not an error.
	empty := NewSource(catalog, fixedResolver{}, &fixedChain{})
	if _, ok, err := empty.Next(context.Background(), "active", "public"); err != nil || ok {
		t.Fatalf("an empty chain was not a clean miss: ok=%v err=%v", ok, err)
	}
}

type fixedChain struct {
	routes   []relayapp.ModelRoute
	degraded []string
}

func (chain *fixedChain) Chain(string) []relayapp.ModelRoute { return chain.routes }
func (chain *fixedChain) Degrade(providerID string) {
	chain.degraded = append(chain.degraded, providerID)
}

func TestExplicitRouteToDisabledProviderFailsClosed(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "active", Name: "Active", BaseURL: "https://active.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	disabled, _ := providerdomain.New(providerdomain.Params{ID: "disabled", Name: "Disabled", BaseURL: "https://disabled.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: false})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active, disabled}, active.ID)
	source := NewSource(catalog, fixedResolver{route: relayapp.ModelRoute{ProviderID: disabled.ID, UpstreamModel: "private-model"}}, nil)
	if _, err := source.Current(context.Background(), "public-model"); !errors.Is(err, providerapp.ErrProviderUnavailable) {
		t.Fatalf("disabled route silently fell back to the active provider: %v", err)
	}
}

func TestUnavailableRouteStoreDoesNotFallBackToActiveProvider(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "active", Name: "Active", BaseURL: "https://active.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active}, active.ID)
	routeErr := errors.New("route store unavailable")
	source := NewSource(catalog, fixedResolver{err: routeErr}, nil)
	if _, err := source.Current(context.Background(), "public-model"); !errors.Is(err, routeErr) {
		t.Fatalf("unavailable route store silently fell back to the active provider: %v", err)
	}
}

func codexProvider(t *testing.T) providerdomain.Provider {
	t.Helper()
	provider, err := providerdomain.New(providerdomain.Params{
		ID: "codex", Name: "Codex", BaseURL: "https://chatgpt.com/backend-api/codex",
		AuthMode: providerdomain.AuthBearer, Enabled: true, Format: "responses",
		Preset: providerdomain.PresetCodex, AccountID: providerdomain.AccountID("acct-123"),
	})
	if err != nil {
		t.Fatalf("codex provider was not valid: %v", err)
	}
	return provider
}

// The codex preset owns its wire identity: User-Agent and originator headers
// plus the account id, and a fixed Responses path. A custom provider keeps
// none of these — the form stays the only source of its headers.
func TestTheCodexPresetCarriesItsIdentityOnEveryRoute(t *testing.T) {
	codex := codexProvider(t)
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{codex}, codex.ID)
	source := NewSource(catalog, fixedResolver{}, nil)
	source.SetAppVersion("1.0.47")
	route, err := source.Pinned(context.Background(), "codex", "gpt-5")
	if err != nil {
		t.Fatalf("codex route failed: %v", err)
	}
	if route.ExtraHeaders["User-Agent"] != "Codex Desktop/1.0.47 ("+runtime.GOOS+"; "+runtime.GOARCH+")" {
		t.Fatalf("codex User-Agent lost the product identity: %q", route.ExtraHeaders["User-Agent"])
	}
	if route.ExtraHeaders["originator"] != "Codex Desktop" {
		t.Fatalf("codex originator missing: %q", route.ExtraHeaders["originator"])
	}
	if route.ExtraHeaders["Chatgpt-Account-Id"] != "acct-123" {
		t.Fatalf("codex account id missing: %q", route.ExtraHeaders["Chatgpt-Account-Id"])
	}
	if route.ResponsesPath != "/responses" {
		t.Fatalf("codex responses path missing: %q", route.ResponsesPath)
	}
	if !route.ResponsesStateless {
		t.Fatalf("the codex route does not ask for the stateless wire contract")
	}
}

// A preset account id is optional until login finishes — the route still
// carries the identity headers, just without Chatgpt-Account-Id.
func TestACodexProviderWithoutAnAccountSkipsTheAccountHeader(t *testing.T) {
	codex, err := providerdomain.New(providerdomain.Params{
		ID: "codex", Name: "Codex", BaseURL: "https://chatgpt.com/backend-api/codex",
		AuthMode: providerdomain.AuthBearer, Enabled: true, Format: "responses",
		Preset: providerdomain.PresetCodex,
	})
	if err != nil {
		t.Fatalf("codex provider without account was not valid: %v", err)
	}
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{codex}, codex.ID)
	source := NewSource(catalog, fixedResolver{}, nil)
	source.SetAppVersion("1.0.47")
	route, err := source.Pinned(context.Background(), "codex", "gpt-5")
	if err != nil {
		t.Fatalf("codex route failed: %v", err)
	}
	if _, present := route.ExtraHeaders["Chatgpt-Account-Id"]; present {
		t.Fatalf("an empty account id still produced a header: %v", route.ExtraHeaders)
	}
	if route.ExtraHeaders["User-Agent"] == "" || route.ExtraHeaders["originator"] == "" {
		t.Fatalf("identity headers were dropped with the account: %v", route.ExtraHeaders)
	}
}

// Without a version handed over by bootstrap the identity still names the
// build as "dev" — never an empty User-Agent.
func TestTheCodexIdentityNeverSendsAnEmptyVersion(t *testing.T) {
	codex := codexProvider(t)
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{codex}, codex.ID)
	source := NewSource(catalog, fixedResolver{}, nil)
	route, err := source.Pinned(context.Background(), "codex", "gpt-5")
	if err != nil {
		t.Fatalf("codex route failed: %v", err)
	}
	if !strings.HasPrefix(route.ExtraHeaders["User-Agent"], "Codex Desktop/dev (") {
		t.Fatalf("missing version did not fall back to dev: %q", route.ExtraHeaders["User-Agent"])
	}
}

// A custom provider must not inherit any preset identity: no extra headers,
// no owned path. Otherwise the form's forbidden-header rules would mean
// nothing for entries the owner never wrote.
func TestACustomProviderCarriesNoPresetIdentity(t *testing.T) {
	custom, _ := providerdomain.New(providerdomain.Params{ID: "custom", Name: "Custom", BaseURL: "https://custom.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{custom}, custom.ID)
	source := NewSource(catalog, fixedResolver{}, nil)
	source.SetAppVersion("1.0.47")
	route, err := source.Pinned(context.Background(), "custom", "model")
	if err != nil {
		t.Fatalf("custom route failed: %v", err)
	}
	if len(route.ExtraHeaders) != 0 || route.ResponsesPath != "" {
		t.Fatalf("a custom provider inherited preset identity: headers=%v path=%q", route.ExtraHeaders, route.ResponsesPath)
	}
	if route.ResponsesStateless {
		t.Fatalf("a custom provider inherited the stateless wire contract")
	}
}
