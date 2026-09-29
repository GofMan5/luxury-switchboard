package domain

import "time"

// The backup document is plain JSON on purpose: the operator asked for a file
// that opens anywhere, without a keyring or a passphrase. The trade is stated
// in the file itself and in the UI — the secrets travel in the clear, so the
// file is exactly as secret as wherever it is put.
const FormatVersion = 1

// KeyEntry carries one credential, revealed. Re-adding it on another machine
// derives the same stable key ID, so an import lands where the export stood.
type KeyEntry struct {
	ProviderID string `json:"providerId"`
	Label      string `json:"label"`
	Secret     string `json:"secret"`
	RPM        int    `json:"rpm"`
	Priority   int    `json:"priority"`
	ProxyURL   string `json:"proxyUrl,omitempty"`
}

// ProviderEntry mirrors one provider profile whole, identity included: keys
// and routes reference it by ID, so the ID is part of the backup.
type ProviderEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	BaseURL     string `json:"baseUrl"`
	AuthMode    string `json:"authMode"`
	AuthHeader  string `json:"authHeader,omitempty"`
	Dialect     string `json:"dialect"`
	Format      string `json:"format"`
	ChatPath    string `json:"chatPath,omitempty"`
	ModelsPath  string `json:"modelsPath,omitempty"`
	ImageCompat bool   `json:"imageCompat"`
	RPM         int    `json:"rpm"`
	CacheTTL    string `json:"cacheTtl,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// Document is the whole portable configuration.
type Document struct {
	Version    int             `json:"version"`
	ExportedAt time.Time       `json:"exportedAt"`
	Note       string          `json:"note"`
	Providers  []ProviderEntry `json:"providers"`
	Keys       []KeyEntry      `json:"keys"`
	Routes     []RouteEntry    `json:"routes"`
	// Prices is the analytics price catalog: market rates, not secrets, so it
	// travels in the clear with everything else. The field is optional and the
	// format version stays 1 — a backup written before prices existed imports
	// as one written after, with an empty catalog left as it is.
	Prices []PriceEntry `json:"prices,omitempty"`
}

// RouteEntry is one assignment, the same shape the routes slice persists;
// relay chains restore entry by entry because a row is keyed by model and
// provider together.
type RouteEntry struct {
	Target          string   `json:"target"`
	PublicModel     string   `json:"publicModel"`
	UpstreamModel   string   `json:"upstreamModel"`
	ProviderID      string   `json:"providerId"`
	ContextLimitKiB int      `json:"contextLimitKiB"`
	Aliases         []string `json:"aliases,omitempty"`
	Enabled         bool     `json:"enabled"`
	Priority        int      `json:"priority"`
}

// PriceEntry is one model's per-million rates, the same fields the analytics
// catalog validates on entry. A restore overwrites whatever rate is set: a
// price is a setting, not an identity, so there is no "already exists" to
// skip.
type PriceEntry struct {
	Model       string  `json:"model"`
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cachedInput"`
	Output      float64 `json:"output"`
	Reasoning   float64 `json:"reasoning"`
}

// ImportReport says what a restore did. Entries that already existed — the
// same provider ID, the same derived key ID, the same route row — are counted
// as skipped, not errors: restoring over a live setup must be repeatable.
type ImportReport struct {
	ProvidersAdded   int `json:"providersAdded"`
	KeysAdded        int `json:"keysAdded"`
	RoutesAdded      int `json:"routesAdded"`
	PricesRestored   int `json:"pricesRestored"`
	ProvidersSkipped int `json:"providersSkipped"`
	KeysSkipped      int `json:"keysSkipped"`
	RoutesSkipped    int `json:"routesSkipped"`
	Failed           int `json:"failed"`
}
