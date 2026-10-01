package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

func testCatalog(t *testing.T, provider providerdomain.Provider) *providerapp.Catalog {
	t.Helper()
	catalog, err := providerapp.NewCatalog([]providerdomain.Provider{provider}, provider.ID)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	return catalog
}

func credential(t *testing.T, secret string) domain.Credential {
	t.Helper()
	value, err := domain.NewCredential(secret)
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	return value
}

// The probe asks for the path the provider actually serves its catalog on.
// Discovery reads provider.ModelsPath; the probe used to guess
// baseURL+"models", and a provider with the catalog elsewhere answered 404 —
// which the pool check filed as a success, resetting the streak of a dead key.
func TestTheProbeAsksForTheConfiguredCatalogPath(t *testing.T) {
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		if request.URL.Path == "/api/catalog" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	prober := NewProber(testCatalog(t, providerdomain.Provider{Enabled: true,
		ID: "llm", AuthMode: providerdomain.AuthBearer, Dialect: providerdomain.DialectOpenAI,
		BaseURL: base, ModelsPath: "/api/catalog",
	}))

	if status := prober.ProbeKey(context.Background(), "llm", domain.Key{Credential: credential(t, "secret")}); status != http.StatusUnauthorized {
		t.Fatalf("the probe did not read the configured catalog path: status=%d", status)
	}
	if hits != 1 {
		t.Fatalf("expected one probe, saw %d", hits)
	}
}

// A base of /v1 with a catalog at /v1/models must not become /v1/v1/models:
// the probe joins the path the way the relay's own requests do.
func TestTheProbeJoinsTheCatalogPathLikeTheRelayDoes(t *testing.T) {
	seen := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request.URL.Path
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL + "/v1")
	prober := NewProber(testCatalog(t, providerdomain.Provider{Enabled: true,
		ID: "llm", AuthMode: providerdomain.AuthBearer, Dialect: providerdomain.DialectOpenAI,
		BaseURL: base, ModelsPath: "/v1/models",
	}))

	prober.ProbeKey(context.Background(), "llm", domain.Key{Credential: credential(t, "secret")})
	if seen != "/v1/models" {
		t.Fatalf("the catalog path was joined wrong: %q", seen)
	}
}

// Auto auth on an Anthropic-dialect provider speaks the header that dialect
// answers to, the same way the relay's own auth decides: a probe that
// authenticates differently files 401s for keys the relay serves fine.
func TestAutoAuthOnAnAnthropicDialectSendsTheHeaderTheRelaySends(t *testing.T) {
	received := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.Header.Get("X-Api-Key") + "|" + request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	prober := NewProber(testCatalog(t, providerdomain.Provider{Enabled: true,
		ID: "llm", AuthMode: providerdomain.AuthAuto, Dialect: providerdomain.DialectAnthropic,
		BaseURL: base, ModelsPath: "/v1/models",
	}))

	prober.ProbeKey(context.Background(), "llm", domain.Key{Credential: credential(t, "anthropic-secret")})
	if received != "anthropic-secret|" {
		t.Fatalf("auto auth did not borrow the anthropic shape: %q", received)
	}

	// An OpenAI-dialect provider still gets the Bearer the OpenAI world expects.
	openAI, _ := url.Parse(upstream.URL)
	openAIProber := NewProber(testCatalog(t, providerdomain.Provider{Enabled: true,
		ID: "llm", AuthMode: providerdomain.AuthAuto, Dialect: providerdomain.DialectOpenAI,
		BaseURL: openAI, ModelsPath: "/v1/models",
	}))
	received = ""
	openAIProber.ProbeKey(context.Background(), "llm", domain.Key{Credential: credential(t, "openai-secret")})
	if received != "|Bearer openai-secret" {
		t.Fatalf("auto auth forgot the OpenAI shape: %q", received)
	}
}

// A pool whose provider is only reachable through the key's proxy used to get
// status zero from every check: the probe ignored the proxy entirely, so
// keys.check was a no-op exactly for the setups that needed it most.
func TestTheProbeTravelsThroughTheKeysOwnProxy(t *testing.T) {
	proxyHits := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		proxyHits++
		// The client speaks absolute-form to a proxy; answering here stands in
		// for the provider on the far side.
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer proxy.Close()
	providerHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		providerHits++
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	prober := NewProber(testCatalog(t, providerdomain.Provider{Enabled: true,
		ID: "hidden", AuthMode: providerdomain.AuthBearer, Dialect: providerdomain.DialectOpenAI,
		BaseURL: base, ModelsPath: "/v1/models",
	}))

	status := prober.ProbeKey(context.Background(), "hidden", domain.Key{Credential: credential(t, "secret"), ProxyURL: proxy.URL})
	if status != http.StatusUnauthorized {
		t.Fatalf("the probe did not read the answer through the proxy: status=%d", status)
	}
	if proxyHits != 1 {
		t.Fatalf("the key's proxy was not used: %d hits", proxyHits)
	}
	if providerHits != 0 {
		t.Fatalf("the probe bypassed the proxy and reached the provider directly: %d hits", providerHits)
	}
}

// An unusable proxy URL reads as "no answer" — the provider is unreachable
// through that egress, which says nothing about the credential.
func TestAnUnusableProxyReadsAsNoAnswer(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:9/v1")
	prober := NewProber(testCatalog(t, providerdomain.Provider{Enabled: true,
		ID: "llm", AuthMode: providerdomain.AuthBearer, Dialect: providerdomain.DialectOpenAI,
		BaseURL: base, ModelsPath: "/v1/models",
	}))
	if status := prober.ProbeKey(context.Background(), "llm", domain.Key{Credential: credential(t, "secret"), ProxyURL: "not a url"}); status != 0 {
		t.Fatalf("an unusable proxy was filed as a verdict: status=%d", status)
	}
}

// A redirect is the provider's own answer, and it says nothing about the
// credential: the verdict a followed redirect would produce is the target's —
// a captive portal's 200 resetting a dead key's streak — and following it
// would carry the key to a host the owner never configured. The probe reads
// the provider's own 3xx and files "no answer", like every other client in
// the tree.
func TestARedirectIsTheProvidersAnswerNotTheKeysVerdict(t *testing.T) {
	var targetHits int
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetHits++
		writer.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", redirectTarget.URL)
		writer.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	prober := NewProber(testCatalog(t, providerdomain.Provider{Enabled: true,
		ID: "llm", AuthMode: providerdomain.AuthBearer, Dialect: providerdomain.DialectOpenAI,
		BaseURL: base, ModelsPath: "/v1/models",
	}))

	if status := prober.ProbeKey(context.Background(), "llm", domain.Key{Credential: credential(t, "secret")}); status != 0 {
		t.Fatalf("the redirect target's answer was filed as the key's verdict: status=%d", status)
	}
	if targetHits != 0 {
		t.Fatalf("the probe followed the redirect and carried the credential to an unconfigured host: %d hits", targetHits)
	}
}
