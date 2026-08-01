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
)

type Provider struct {
	ID          string
	Name        string
	BaseURL     *url.URL
	AuthMode    AuthMode
	AuthHeader  string
	Dialect     Dialect
	ModelsPath  string
	ImageCompat bool
	RPM         int
	CacheTTL    time.Duration
	Enabled     bool
	Builtin     bool
}

type Params struct {
	ID          string
	Name        string
	BaseURL     string
	AuthMode    AuthMode
	AuthHeader  string
	Dialect     Dialect
	ModelsPath  string
	ImageCompat bool
	RPM         int
	CacheTTL    time.Duration
	Enabled     bool
	Builtin     bool
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
	params.AuthHeader = strings.TrimSpace(params.AuthHeader)
	if params.AuthMode == AuthCustom && !validAuthHeader(params.AuthHeader) {
		return Provider{}, errors.New("invalid custom auth header")
	}
	if params.RPM < 0 || params.RPM > MaxRPM {
		return Provider{}, errors.New("provider RPM is out of range")
	}
	if params.CacheTTL != 0 && params.CacheTTL != time.Hour {
		return Provider{}, errors.New("provider cache TTL is unsupported")
	}
	return Provider{
		ID:          params.ID,
		Name:        params.Name,
		BaseURL:     parsed,
		AuthMode:    params.AuthMode,
		AuthHeader:  params.AuthHeader,
		Dialect:     params.Dialect,
		ModelsPath:  params.ModelsPath,
		ImageCompat: params.ImageCompat,
		RPM:         params.RPM,
		CacheTTL:    params.CacheTTL,
		Enabled:     params.Enabled,
		Builtin:     params.Builtin,
	}, nil
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
	ImageCompat   bool   `json:"imageCompat"`
	RPM           int    `json:"rpm"`
	CacheTTL      string `json:"cacheTtl"`
	Enabled       bool   `json:"enabled"`
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
		ImageCompat: provider.ImageCompat,
		RPM:         provider.RPM,
		CacheTTL:    provider.CacheTTL.String(),
		Enabled:     provider.Enabled,
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
