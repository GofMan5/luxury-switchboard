package probe

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// Prober checks one credential against its provider's catalog endpoint. It
// borrows the provider's own configuration — the models path the catalog
// actually lives at, the auth shape the relay applies to the same request, the
// key's own proxy — so the answer means the same thing a real request would.
type Prober struct {
	catalog *providerapp.Catalog
	client  *http.Client
	mu      sync.Mutex
	proxies map[string]*http.Client
}

// maxProberProxyClients bounds the per-proxy client cache. Proxy edits are
// rare, so a bounded full eviction is simpler and safer than retaining idle
// sockets forever.
const maxProberProxyClients = 16

func NewProber(catalog *providerapp.Catalog) *Prober {
	return &Prober{
		catalog: catalog,
		client:  &http.Client{Timeout: 12 * time.Second, CheckRedirect: useLastResponse},
		proxies: make(map[string]*http.Client),
	}
}

// useLastResponse is the relay's own redirect posture: every other client in
// this tree refuses to follow a redirect, because the provider's 3xx is the
// provider's answer, not a hop to wherever it points. A probe that followed
// would file the redirect target's status as a verdict — a captive portal's
// 200 resetting a dead key's streak — and would carry the x-api-key or custom
// auth header to a host the owner never configured, since Go only strips the
// Authorization family on cross-host hops.
func useLastResponse(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// ProbeKey returns the HTTP status the provider answered for this key, or zero
// when nothing answered. Passthrough providers hold no credential of their
// own, so they read as "nothing to check" rather than probing with nothing.
// A key whose own egress is unusable also reads as zero: the provider being
// unreachable through that proxy says nothing about the credential.
func (prober *Prober) ProbeKey(ctx context.Context, providerID string, key domain.Key) int {
	provider, exists := prober.catalog.Get(providerID)
	if !exists || string(provider.AuthMode) == string(providerdomain.AuthPassthrough) {
		return 0
	}
	// The catalog path the provider actually serves — discovery reads this
	// same field — not a guess at where "models" lives. A provider whose
	// catalog is not at baseURL+"models" answered 404, and a 404 filed as a
	// success reset the streak of a dead key.
	target := *provider.BaseURL
	target.Path = providerdomain.JoinCatalogPath(target.Path, provider.ModelsPath)
	target.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return 0
	}
	applyAuth(request, provider, key.Credential.Reveal())
	client := prober.clientForProxy(key.ProxyURL)
	if client == nil {
		return 0
	}
	response, err := client.Do(request)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4*1024))
	response.Body.Close()
	// A redirect is the provider's own answer, and it says nothing about the
	// credential: the verdict it would produce is the target's, not the
	// provider's. Read as "no answer", like an unreachable egress.
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return 0
	}
	return response.StatusCode
}

func applyAuth(request *http.Request, provider providerdomain.Provider, secret string) {
	switch provider.AuthMode {
	case providerdomain.AuthAPIKey:
		request.Header.Set("X-Api-Key", secret)
	case providerdomain.AuthCustom:
		if provider.AuthHeader != "" {
			request.Header.Set(provider.AuthHeader, secret)
			return
		}
		request.Header.Set("Authorization", "Bearer "+secret)
	default:
		// Auto sniffs the dialect the way the relay's own auth does for the
		// same catalog request: an Anthropic-dialect provider answers
		// x-api-key, everything else Bearer. A probe that authenticates
		// differently from real traffic files 401s for keys the relay serves
		// fine, and those grow a dead-key streak out of nothing.
		if provider.Dialect == providerdomain.DialectAnthropic {
			request.Header.Set("x-api-key", secret)
			return
		}
		request.Header.Set("Authorization", "Bearer "+secret)
	}
}

// clientForProxy returns the client that carries this key's own egress, or nil
// when the configured proxy is not a usable forwarding URL. The schemes the
// key domain accepts are the schemes accepted here, named once in
// keypool/domain, so a key behaves alike on every path.
func (prober *Prober) clientForProxy(rawURL string) *http.Client {
	if rawURL == "" {
		return prober.client
	}
	proxyURL, err := url.Parse(rawURL)
	if err != nil || proxyURL.Host == "" || !domain.ProxySchemeAllowed(proxyURL.Scheme) {
		return nil
	}
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if client := prober.proxies[rawURL]; client != nil {
		return client
	}
	if len(prober.proxies) >= maxProberProxyClients {
		for key, client := range prober.proxies {
			client.CloseIdleConnections()
			delete(prober.proxies, key)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Timeout: 12 * time.Second, Transport: transport}
	prober.proxies[rawURL] = client
	return client
}
