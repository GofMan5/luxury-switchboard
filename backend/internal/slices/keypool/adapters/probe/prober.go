package probe

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

// Prober checks one credential against its provider's catalog endpoint. It
// borrows the provider's auth shape the same way the relay applies it, so the
// answer means the same thing a real request would.
type Prober struct {
	catalog *providerapp.Catalog
	client  *http.Client
}

func NewProber(catalog *providerapp.Catalog) *Prober {
	return &Prober{catalog: catalog, client: &http.Client{Timeout: 12 * time.Second}}
}

// ProbeKey returns the HTTP status the provider answered for this key, or zero
// when nothing answered. Passthrough providers hold no credential of their
// own, so they read as "nothing to check" rather than probing with nothing.
func (prober *Prober) ProbeKey(ctx context.Context, providerID string, key domain.Key) int {
	provider, exists := prober.catalog.Get(providerID)
	if !exists || string(provider.AuthMode) == string(providerdomain.AuthPassthrough) {
		return 0
	}
	endpoint := provider.BaseURL.JoinPath("models")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0
	}
	applyAuth(request, provider, key.Credential.Reveal())
	response, err := prober.client.Do(request)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4*1024))
	response.Body.Close()
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
		// Bearer and auto both speak the header the OpenAI world expects.
		request.Header.Set("Authorization", "Bearer "+secret)
	}
}
