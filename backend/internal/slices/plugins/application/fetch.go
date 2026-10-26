package application

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

const (
	MaxFetchURLBytes       = 2048
	MaxFetchHeaders        = 32
	MaxFetchHeaderBytes    = 8 * 1024
	MaxFetchRequestBytes   = 64 * 1024
	MaxFetchResponseBytes  = 192 * 1024
	FetchesPerMinute       = 60
	MaxConcurrentFetches   = 4
	DefaultFetchTimeout    = 15 * time.Second
	MinFetchTimeout        = time.Second
	MaxFetchTimeout        = 30 * time.Second
	redactedSecret         = "[secret]"
	fetchUserAgentTemplate = "LuxurySwitchboard-Plugin/%s (%s)"
)

// Errors a FetchTransport reports. Anything else it returns is a network
// failure.
var (
	ErrBlockedAddress = errors.New("the address is not reachable from plugins")
	ErrFetchTimeout   = errors.New("the request timed out")
	ErrNetwork        = errors.New("the request failed")
)

// FetchError is a refused or failed plugins.fetch. Code is one of the
// neutral codes the worker sees; Message never names an address or a value.
type FetchError struct {
	Code    string
	Message string
}

func (err FetchError) Error() string { return err.Message }

var (
	fetchNotAllowed     = FetchError{"not_allowed", "This plugin may not fetch that address"}
	fetchBlocked        = FetchError{"blocked_address", "That address is not reachable from plugins"}
	fetchTimedOut       = FetchError{"timeout", "The request timed out"}
	fetchTooLarge       = FetchError{"too_large", "The request or response is too large"}
	fetchRateLimited    = FetchError{"rate_limited", "Too many requests from this plugin; wait and retry"}
	fetchNetwork        = FetchError{"network", "The request failed"}
	fetchMissingSecret  = FetchError{"missing_secret", "A secret this request uses is not set"}
	fetchInvalidRequest = FetchError{"invalid_plugin", "Plugin fetch request is malformed"}
)

// OutboundRequest is a checked request ready for the network: the host is
// allowed, the headers are final and every placeholder is filled.
type OutboundRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// InboundResponse is what the network answered. Body holds at most the
// requested limit plus one byte, so an oversized answer is recognisable.
type InboundResponse struct {
	Status  int
	Headers map[string][]string
	Body    []byte
}

// FetchTransport performs one request without following redirects. It
// refuses blocked addresses with ErrBlockedAddress, maps deadlines to
// ErrFetchTimeout and reads at most maxBody+1 bytes of the answer.
type FetchTransport interface {
	Fetch(ctx context.Context, request OutboundRequest, maxBody int) (InboundResponse, error)
}

// FetchRequest is plugins.fetch as the shell sends it on a plugin's behalf.
type FetchRequest struct {
	ID        string            `json:"id"`
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	TimeoutMS int               `json:"timeoutMs"`
}

type FetchResult struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	Redirected bool              `json:"redirected"`
}

// Fetcher runs plugins.fetch: the network allowlist, secret placeholders,
// header rules, rate limits and redaction around a FetchTransport.
type Fetcher struct {
	plugins   *Service
	transport FetchTransport
	limits    *limiter
}

func NewFetcher(plugins *Service, transport FetchTransport) (*Fetcher, error) {
	if plugins == nil || transport == nil {
		return nil, errors.New("plugin fetch needs the plugins service and a transport")
	}
	return &Fetcher{plugins: plugins, transport: transport, limits: newLimiter(FetchesPerMinute, MaxConcurrentFetches)}, nil
}

var (
	fetchMethods      = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	headerToken       = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,64}$")
	secretPlaceholder = regexp.MustCompile(`\{\{secret:([A-Za-z0-9_]{1,32})\}\}`)
	forbiddenHeaders  = []string{"host", "content-length", "connection", "cookie", "transfer-encoding", "upgrade", "te", "trailer", "user-agent", "keep-alive", "expect"}
	forbiddenPrefixes = []string{"proxy-", "x-switchboard-", "x-forwarded-"}
)

func (fetcher *Fetcher) Fetch(ctx context.Context, request FetchRequest) (FetchResult, error) {
	access, err := fetcher.plugins.fetchAccess(request.ID)
	if err != nil {
		return FetchResult{}, err
	}
	method := cmp.Or(request.Method, "GET")
	timeout, ok := fetchTimeout(request.TimeoutMS)
	if !slices.Contains(fetchMethods, method) || !ok || len(request.Headers) > MaxFetchHeaders {
		return FetchResult{}, fetchInvalidRequest
	}
	target, err := allowedURL(request.URL, access.network)
	if err != nil {
		return FetchResult{}, err
	}
	if len(request.Body) > MaxFetchRequestBytes {
		return FetchResult{}, fetchTooLarge
	}
	headers := map[string]string{}
	for name, value := range request.Headers {
		if !headerToken.MatchString(name) || len(value) > MaxFetchHeaderBytes {
			return FetchResult{}, fetchInvalidRequest
		}
		if forbiddenHeader(strings.ToLower(name)) {
			continue
		}
		filled, err := fillSecrets(value, access.secrets)
		if err != nil {
			return FetchResult{}, err
		}
		if len(filled) > MaxFetchHeaderBytes || strings.ContainsAny(filled, "\r\n\x00") || !utf8.ValidString(filled) {
			return FetchResult{}, fetchInvalidRequest
		}
		headers[name] = filled
	}
	headers["User-Agent"] = fmt.Sprintf(fetchUserAgentTemplate, fetcher.plugins.appVersion, request.ID)
	body, err := fillSecrets(request.Body, access.secrets)
	if err != nil {
		return FetchResult{}, err
	}
	if len(body) > MaxFetchRequestBytes {
		return FetchResult{}, fetchTooLarge
	}
	release, admitted := fetcher.limits.acquire(request.ID)
	if !admitted {
		return FetchResult{}, fetchRateLimited
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer, err := fetcher.transport.Fetch(ctx, OutboundRequest{Method: method, URL: target, Headers: headers, Body: []byte(body)}, MaxFetchResponseBytes)
	switch {
	case errors.Is(err, ErrBlockedAddress):
		return FetchResult{}, fetchBlocked
	case errors.Is(err, ErrFetchTimeout):
		return FetchResult{}, fetchTimedOut
	case err != nil:
		return FetchResult{}, fetchNetwork
	case len(answer.Body) > MaxFetchResponseBytes:
		return FetchResult{}, fetchTooLarge
	}
	result := FetchResult{Status: answer.Status, Headers: responseHeaders(answer.Headers, access.secrets), Body: redact(strings.ToValidUTF8(string(answer.Body), "�"), access.secrets)}
	if fits, _ := fetcher.plugins.fits(result); !fits {
		return FetchResult{}, fetchTooLarge
	}
	return result, nil
}

// fetchAccess is what a fetch may use: the plugin's network allowlist and
// its secret values. Every refusal reads the same, so a worker cannot tell an
// absent plugin from a missing grant.
type fetchAccess struct {
	network domain.Network
	secrets map[string]string
}

func (service *Service) fetchAccess(id string) (fetchAccess, error) {
	if !service.Granted(id, domain.PermissionNetFetch) {
		return fetchAccess{}, fetchNotAllowed
	}
	entry, err := service.lookup(id)
	if err != nil || entry.plugin.Package.Network == nil {
		return fetchAccess{}, fetchNotAllowed
	}
	// The record is replaced, never edited, so these stay as they were read.
	return fetchAccess{network: *entry.plugin.Package.Network, secrets: entry.plugin.Secrets}, nil
}

func fetchTimeout(milliseconds int) (time.Duration, bool) {
	if milliseconds == 0 {
		return DefaultFetchTimeout, true
	}
	timeout := time.Duration(milliseconds) * time.Millisecond
	return timeout, timeout >= MinFetchTimeout && timeout <= MaxFetchTimeout
}

// allowedURL returns the URL to request when it is https on the default port
// to a host the plugin declared. The answer is re-encoded, so what is
// checked is exactly what is requested.
func allowedURL(raw string, network domain.Network) (string, error) {
	if len(raw) > MaxFetchURLBytes {
		return "", fetchNotAllowed
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Opaque != "" {
		return "", fetchNotAllowed
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", fetchNotAllowed
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || domain.HostProblem(host) != "" || !network.Allows(host) {
		return "", fetchNotAllowed
	}
	parsed.Host = host
	parsed.Fragment = ""
	return parsed.String(), nil
}

func forbiddenHeader(name string) bool {
	if slices.Contains(forbiddenHeaders, name) {
		return true
	}
	return slices.ContainsFunc(forbiddenPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

func fillSecrets(text string, secrets map[string]string) (string, error) {
	missing := false
	filled := secretPlaceholder.ReplaceAllStringFunc(text, func(placeholder string) string {
		value := secrets[secretPlaceholder.FindStringSubmatch(placeholder)[1]]
		if value == "" {
			missing = true
		}
		return value
	})
	if missing {
		return "", fetchMissingSecret
	}
	return filled, nil
}

// responseHeaders lower-cases names, joins repeated values, drops location
// (redirects are never followed) and keeps the first MaxFetchHeaders by name.
func responseHeaders(headers map[string][]string, secrets map[string]string) map[string]string {
	joined := map[string]string{}
	for name, values := range headers {
		name = strings.ToLower(name)
		if name == "location" || len(values) == 0 {
			continue
		}
		value := strings.Join(values, ", ")
		if previous, exists := joined[name]; exists {
			value = previous + ", " + value
		}
		joined[name] = value
	}
	names := slices.Sorted(maps.Keys(joined))
	result := make(map[string]string, min(len(names), MaxFetchHeaders))
	for _, name := range names[:min(len(names), MaxFetchHeaders)] {
		result[name] = redact(strings.ToValidUTF8(joined[name], "�"), secrets)
	}
	return result
}

// redact replaces every secret value and its base64 and JSON-escaped
// forms, longest first so a value containing another is not left half
// visible.
func redact(text string, secrets map[string]string) string {
	forms := map[string]bool{}
	for _, value := range secrets {
		if value == "" {
			continue
		}
		forms[value] = true
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			forms[encoding.EncodeToString([]byte(value))] = true
		}
		forms[jsonEscaped(value, true)] = true
		forms[jsonEscaped(value, false)] = true
		if len(value) < minEncodedSecret {
			continue
		}
		escaped := jsonEscaped(value, false)
		slashed := strings.ReplaceAll(escaped, "/", `\/`) // PHP's default
		for _, form := range []string{
			hex.EncodeToString([]byte(value)), strings.ToUpper(hex.EncodeToString([]byte(value))),
			url.QueryEscape(value), url.PathEscape(value),
			slashed, asciiEscaped(escaped), asciiEscaped(slashed),
		} {
			forms[form] = true
		}
		for _, form := range alignedBase64(value) {
			if len(form) >= minAlignedBase64 { // shorter fragments match ordinary words
				forms[form] = true
			}
		}
	}
	values := slices.Collect(maps.Keys(forms))
	slices.SortFunc(values, func(left, right string) int { return cmp.Compare(len(right), len(left)) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, redactedSecret)
	}
	return text
}

// Encoded forms of shorter secrets would mostly match ordinary text; the raw
// value is still redacted at every length.
const minEncodedSecret = 4

const minAlignedBase64 = 8

// alignedBase64 is value as base64 inside a longer encoded run, such as
// Basic base64("user:" + secret), at each of the three byte alignments, with
// the boundary characters that also carry neighbouring bytes trimmed.
func alignedBase64(value string) []string {
	var forms []string
	for _, encoding := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for offset, drop := range []int{0, 2, 3} {
			encoded := encoding.EncodeToString(append(make([]byte, offset), value...))[drop:]
			if (offset+len(value))%3 != 0 {
				encoded = encoded[:len(encoded)-1]
			}
			forms = append(forms, encoded)
		}
	}
	return forms
}

// asciiEscaped is a JSON-escaped string with every non-ASCII character as a
// \uXXXX escape (Python's default ensure_ascii, for one).
func asciiEscaped(escaped string) string {
	var out strings.Builder
	for _, char := range escaped {
		if char < utf8.RuneSelf {
			out.WriteRune(char)
			continue
		}
		for _, unit := range utf16.Encode([]rune{char}) {
			fmt.Fprintf(&out, `\u%04x`, unit)
		}
	}
	return out.String()
}

// jsonEscaped is value as it appears inside a JSON string, with or without
// HTML escaping (Go escapes <, > and &; most other encoders do not).
func jsonEscaped(value string, escapeHTML bool) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(escapeHTML)
	_ = encoder.Encode(value)
	quoted := bytes.TrimSpace(encoded.Bytes()) // Encode ends with a newline
	return string(quoted[1 : len(quoted)-1])
}
