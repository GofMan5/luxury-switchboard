package domain

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type AuthMode string
type Dialect string
type APIFormat string

// RateUnit is the period the provider's request limit is counted over. Most
// providers publish a per-minute quota, but some cap bursts per second, and a
// per-minute window would let a request storm through the first second.
type RateUnit string

const (
	MaxRPM = 1_000_000

	AuthPassthrough AuthMode = "passthrough"
	AuthAuto        AuthMode = "auto"
	AuthBearer      AuthMode = "bearer"
	AuthAPIKey      AuthMode = "x-api-key"
	AuthCustom      AuthMode = "custom"

	DialectAuto      Dialect = "auto"
	DialectOpenAI    Dialect = "openai"
	DialectAnthropic Dialect = "anthropic"

	FormatAuto      APIFormat = "auto"
	FormatResponses APIFormat = "responses"
	FormatChat      APIFormat = "chat"

	RatePerMinute RateUnit = "minute"
	RatePerSecond RateUnit = "second"

	// PresetCodex marks a provider the codex slice provisioned from a curated
	// preset. The frontend treats it as managed: the identity lives in the
	// OAuth session, not in the form fields. The empty preset is a
	// hand-configured provider, which is every provider that exists today.
	PresetCodex Preset = "codex"
)

// Preset names the curated provider list an entry came from. It is a marker,
// not a mode: the relay profile consults it to attach the preset's identity
// headers, and the manager preserves it across edits so the marker cannot be
// stripped out of a provider the codex slice still manages.
type Preset string

// AccountID is the upstream account identifier of a preset provider (the
// ChatGPT account the codex OAuth session belongs to). It is data the relay
// needs on the wire, so it travels on the provider; it is never a credential.
type AccountID string

type Provider struct {
	ID          string
	Name        string
	BaseURL     *url.URL
	AuthMode    AuthMode
	AuthHeader  string
	Dialect     Dialect
	ModelsPath  string
	Format      APIFormat
	ChatPath    string
	ImageCompat bool
	RPM         int
	RateUnit    RateUnit
	CacheTTL    time.Duration
	Enabled     bool
	Builtin     bool
	Preset      Preset
	AccountID   AccountID
}

type Params struct {
	ID          string
	Name        string
	BaseURL     string
	AuthMode    AuthMode
	AuthHeader  string
	Dialect     Dialect
	ModelsPath  string
	Format      APIFormat
	ChatPath    string
	ImageCompat bool
	RPM         int
	RateUnit    RateUnit
	CacheTTL    time.Duration
	Enabled     bool
	Builtin     bool
	Preset      Preset
	AccountID   AccountID
}

func New(params Params) (Provider, error) {
	params.ID = strings.TrimSpace(params.ID)
	params.Name = strings.TrimSpace(params.Name)
	params.BaseURL = strings.TrimSpace(params.BaseURL)
	if !validText(params.ID, 64) || !validText(params.Name, 80) {
		return Provider{}, errors.New("provider id and name are required")
	}
	if len(params.BaseURL) > 2*1024 {
		return Provider{}, errors.New("provider URL is too long")
	}
	parsed, err := url.Parse(params.BaseURL)
	if err != nil || parsed.Host == "" {
		return Provider{}, errors.New("provider URL must be absolute HTTP(S)")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Provider{}, errors.New("provider URL must be absolute HTTP(S)")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return Provider{}, errors.New("provider URL must not contain credentials or a fragment")
	}
	if sensitiveQuery(parsed) {
		return Provider{}, errors.New("provider credentials must use the encrypted key pool")
	}
	if parsed.Scheme == "http" && !loopbackHost(parsed.Hostname()) {
		return Provider{}, errors.New("remote provider URL must use HTTPS")
	}
	if params.AuthMode != AuthPassthrough && params.AuthMode != AuthAuto && params.AuthMode != AuthBearer && params.AuthMode != AuthAPIKey && params.AuthMode != AuthCustom {
		return Provider{}, errors.New("unsupported provider auth mode")
	}
	if params.Dialect == "" {
		params.Dialect = DialectAuto
	}
	if params.Dialect != DialectAuto && params.Dialect != DialectOpenAI && params.Dialect != DialectAnthropic {
		return Provider{}, errors.New("unsupported provider dialect")
	}
	if params.ModelsPath == "" {
		params.ModelsPath = "/v1/models"
	}
	if !strings.HasPrefix(params.ModelsPath, "/") || strings.ContainsAny(params.ModelsPath, "?#\r\n") || len(params.ModelsPath) > 160 {
		return Provider{}, errors.New("invalid provider models path")
	}
	if params.Format == "" {
		params.Format = FormatAuto
	}
	if params.Format != FormatAuto && params.Format != FormatResponses && params.Format != FormatChat {
		return Provider{}, errors.New("unsupported provider request format")
	}
	if params.ChatPath == "" {
		params.ChatPath = "/v1/chat/completions"
	}
	if !strings.HasPrefix(params.ChatPath, "/") || strings.ContainsAny(params.ChatPath, "?#\r\n") || len(params.ChatPath) > 160 {
		return Provider{}, errors.New("invalid provider chat completions path")
	}
	params.AuthHeader = strings.TrimSpace(params.AuthHeader)
	if params.AuthMode == AuthCustom && !validAuthHeader(params.AuthHeader) {
		return Provider{}, errors.New("invalid custom auth header")
	}
	if params.RPM < 0 || params.RPM > MaxRPM {
		return Provider{}, errors.New("provider RPM is out of range")
	}
	if params.RateUnit == "" {
		params.RateUnit = RatePerMinute
	}
	if params.RateUnit != RatePerMinute && params.RateUnit != RatePerSecond {
		return Provider{}, errors.New("unsupported provider rate unit")
	}
	if params.CacheTTL != 0 && params.CacheTTL != time.Hour {
		return Provider{}, errors.New("provider cache TTL is unsupported")
	}
	params.Preset = Preset(strings.TrimSpace(string(params.Preset)))
	params.AccountID = AccountID(strings.TrimSpace(string(params.AccountID)))
	if params.Preset != "" && params.Preset != PresetCodex {
		return Provider{}, errors.New("unsupported provider preset")
	}
	if params.Preset == PresetCodex {
		if params.AccountID != "" && !validAccountID(params.AccountID) {
			return Provider{}, errors.New("codex account id is malformed")
		}
	} else if params.AccountID != "" {
		return Provider{}, errors.New("account id is only valid on a preset provider")
	}
	return Provider{
		ID:          params.ID,
		Name:        params.Name,
		BaseURL:     parsed,
		AuthMode:    params.AuthMode,
		AuthHeader:  params.AuthHeader,
		Dialect:     params.Dialect,
		ModelsPath:  params.ModelsPath,
		Format:      params.Format,
		ChatPath:    params.ChatPath,
		ImageCompat: params.ImageCompat,
		RPM:         params.RPM,
		RateUnit:    params.RateUnit,
		CacheTTL:    params.CacheTTL,
		Enabled:     params.Enabled,
		Builtin:     params.Builtin,
		Preset:      params.Preset,
		AccountID:   params.AccountID,
	}, nil
}

func validAccountID(value AccountID) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9':
		case character == '-' || character == '_' || character == '.':
		default:
			return false
		}
	}
	return true
}

// RateWindow returns the period the provider's request limit is counted over.
// Providers persisted before the unit existed carry an empty value and keep the
// per-minute behaviour they were configured with.
func (provider Provider) RateWindow() time.Duration {
	if provider.RateUnit == RatePerSecond {
		return time.Second
	}
	return time.Minute
}

func sensitiveQuery(value *url.URL) bool {
	for name := range value.Query() {
		normalized := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(name))
		switch normalized {
		case "apikey", "xapikey", "subscriptionkey", "key", "token", "accesstoken", "auth", "authorization", "password", "secret", "signature", "sig", "code":
			return true
		}
	}
	return false
}

func validText(value string, limit int) bool {
	if value == "" || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}

type PublicProvider struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	BaseURL       string `json:"baseUrl"`
	AuthMode      string `json:"authMode"`
	AuthHeader    string `json:"authHeader,omitempty"`
	Dialect       string `json:"dialect"`
	ModelsPath    string `json:"modelsPath"`
	Format        string `json:"format"`
	ChatPath      string `json:"chatPath"`
	ImageCompat   bool   `json:"imageCompat"`
	RPM           int    `json:"rpm"`
	RateUnit      string `json:"rateUnit"`
	CacheTTL      string `json:"cacheTtl"`
	Enabled       bool   `json:"enabled"`
	Preset        string `json:"preset,omitempty"`
	KeyConfigured bool   `json:"keyConfigured"`
	KeyCount      int    `json:"keyCount"`
	Builtin       bool   `json:"builtin"`
}

func (provider Provider) Public() PublicProvider {
	return PublicProvider{
		ID:          provider.ID,
		Name:        provider.Name,
		BaseURL:     provider.BaseURL.String(),
		AuthMode:    string(provider.AuthMode),
		AuthHeader:  provider.AuthHeader,
		Dialect:     string(provider.Dialect),
		ModelsPath:  provider.ModelsPath,
		Format:      string(provider.Format),
		ChatPath:    provider.ChatPath,
		ImageCompat: provider.ImageCompat,
		RPM:         provider.RPM,
		RateUnit:    string(provider.RateUnit),
		CacheTTL:    provider.CacheTTL.String(),
		Enabled:     provider.Enabled,
		Preset:      string(provider.Preset),
		Builtin:     provider.Builtin,
	}
}

func validAuthHeader(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			return false
		}
	}
	forbidden := map[string]struct{}{
		"accept": {}, "accept-encoding": {}, "connection": {}, "content-encoding": {},
		"content-length": {}, "content-type": {}, "host": {}, "user-agent": {},
		"proxy-authorization": {}, "transfer-encoding": {}, "upgrade": {},
		"x-provider-switch-tunnel": {}, "x-provider-switch-model": {},
	}
	_, blocked := forbidden[strings.ToLower(value)]
	return !blocked
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
