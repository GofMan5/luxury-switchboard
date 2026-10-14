package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

func TestProxyProbeDoesNotFollowProviderRedirects(t *testing.T) {
	for _, auth := range []providerdomain.AuthMode{providerdomain.AuthAPIKey, providerdomain.AuthCustom} {
		t.Run(string(auth), func(t *testing.T) {
			// This local proxy stands in for both destinations. Any second
			// request is a followed redirect outside the configured catalog.
			redirected := 0
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/catalog" {
					w.Header().Set("Location", "http://127.0.0.1:1/other-provider")
					w.WriteHeader(http.StatusFound)
					return
				}
				redirected++
				w.WriteHeader(http.StatusOK)
			}))
			defer proxy.Close()
			base, _ := url.Parse("http://127.0.0.1:2")
			prober := NewProber(testCatalog(t, providerdomain.Provider{
				ID: "fixture", Enabled: true, BaseURL: base, ModelsPath: "/catalog",
				AuthMode: auth, AuthHeader: "X-Fixture-Key",
			}))
			defer prober.clientForProxy(proxy.URL).CloseIdleConnections()
			key := domain.Key{Credential: credential(t, "synthetic-probe-key"), ProxyURL: proxy.URL}
			// Repeat to exercise the cached client as well as its creation.
			for range 2 {
				if status := prober.ProbeKey(context.Background(), "fixture", key); status != 0 {
					t.Fatalf("redirect destination was mistaken for authentication: %d", status)
				}
			}
			if redirected != 0 {
				t.Fatalf("probe followed %d unconfigured redirects", redirected)
			}
		})
	}
}
