package privacyaudit

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

const (
	maxBodyBytes   = 64 * 1024
	maxHeaderBytes = 32 * 1024
	maxReportBytes = 192 * 1024
	requestTimeout = 20 * time.Second
)

var publisherPath = regexp.MustCompile(`^/model-tunnel/[0-9a-f]{48}/v1$`)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.MaxResponseHeaderBytes = maxHeaderBytes
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	return &Client{http: &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (client *Client) Audit(ctx context.Context, address, token string) (domain.PrivacyReport, error) {
	if client == nil || client.http == nil || len(token) < 32 || len(token) > 512 {
		return domain.PrivacyReport{}, errors.New("privacy audit settings are invalid")
	}
	target, err := modelsURL(address)
	if err != nil {
		return domain.PrivacyReport{}, err
	}
	remoteAddress := ""
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		remoteAddress = info.Conn.RemoteAddr().String()
	}}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, target.String(), nil)
	if err != nil {
		return domain.PrivacyReport{}, errors.New("privacy audit request is invalid")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Cache-Control", "no-cache")

	started := time.Now()
	response, err := client.http.Do(request)
	if err != nil {
		return domain.PrivacyReport{}, errors.New("privacy audit endpoint is unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return domain.PrivacyReport{}, errors.New("privacy audit response is too large")
	}
	bodyBytes := len(body)
	credentialReflected := strings.Contains(string(body), token)
	body = []byte(strings.ReplaceAll(string(body), token, "[redacted access key]"))
	headers, headerReflected, err := responseHeaders(response.Header, token)
	if err != nil {
		return domain.PrivacyReport{}, err
	}
	fields, models, parseError := parseModels(body)
	report := domain.PrivacyReport{
		CheckedAt:           time.Now().UTC().Format(time.RFC3339Nano),
		RequestURL:          target.String(),
		Status:              response.StatusCode,
		StatusText:          response.Status,
		Protocol:            response.Proto,
		RemoteAddress:       remoteAddress,
		DurationMS:          time.Since(started).Milliseconds(),
		BodyBytes:           bodyBytes,
		Headers:             headers,
		TopLevelFields:      fields,
		Models:              models,
		RawBody:             string(body),
		ParseError:          parseError,
		CredentialReflected: credentialReflected || headerReflected,
	}
	if response.TLS != nil {
		report.TLS = tlsReport(response.TLS)
	}
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > maxReportBytes {
		return domain.PrivacyReport{}, errors.New("privacy audit report is too large")
	}
	return report, nil
}

func modelsURL(address string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(address))
	if err != nil || target.Opaque != "" || target.User != nil || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || target.RawPath != "" {
		return nil, errors.New("privacy audit target is invalid")
	}
	path := strings.TrimRight(target.Path, "/")
	switch {
	case target.Scheme == "https" && strings.EqualFold(target.Hostname(), "luxuryprivate.duckdns.org") && (target.Port() == "" || target.Port() == "443") && publisherPath.MatchString(path):
	case target.Scheme == "http" && loopbackHost(target.Hostname()) && path == "/v1":
	default:
		return nil, errors.New("privacy audit target is not trusted")
	}
	target.Path = path + "/models"
	return target, nil
}

func loopbackHost(host string) bool {
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func responseHeaders(source http.Header, token string) ([]domain.PrivacyHeader, bool, error) {
	names := make([]string, 0, len(source))
	total := 0
	for name, values := range source {
		total += len(name)
		for _, value := range values {
			total += len(value)
		}
		if total > maxHeaderBytes {
			return nil, false, errors.New("privacy audit headers are too large")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	headers := make([]domain.PrivacyHeader, 0, len(names))
	credentialReflected := false
	for _, name := range names {
		values := append([]string(nil), source.Values(name)...)
		for index, value := range values {
			if strings.Contains(value, token) {
				credentialReflected = true
				values[index] = strings.ReplaceAll(value, token, "[redacted access key]")
			}
		}
		headers = append(headers, domain.PrivacyHeader{Name: name, Values: values})
	}
	return headers, credentialReflected, nil
}

func parseModels(body []byte) ([]string, []domain.PrivacyModel, string) {
	var document map[string]json.RawMessage
	if json.Unmarshal(body, &document) != nil {
		return nil, nil, "Response body is not valid JSON"
	}
	fields := sortedFields(document)
	var items []json.RawMessage
	if raw, ok := document["data"]; !ok || json.Unmarshal(raw, &items) != nil {
		return fields, nil, "Response does not contain a model list"
	}
	models := make([]domain.PrivacyModel, 0, len(items))
	for _, item := range items {
		var value struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
		}
		var rawFields map[string]json.RawMessage
		if json.Unmarshal(item, &value) != nil || json.Unmarshal(item, &rawFields) != nil {
			return fields, models, "Model list contains an invalid item"
		}
		models = append(models, domain.PrivacyModel{ID: value.ID, Object: value.Object, Created: value.Created, Fields: sortedFields(rawFields)})
	}
	return fields, models, ""
}

func sortedFields(value map[string]json.RawMessage) []string {
	fields := make([]string, 0, len(value))
	for field := range value {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func tlsReport(state *tls.ConnectionState) *domain.PrivacyTLS {
	report := &domain.PrivacyTLS{Version: tls.VersionName(state.Version), CipherSuite: tls.CipherSuiteName(state.CipherSuite), ServerName: state.ServerName}
	if len(state.PeerCertificates) > 0 {
		certificate := state.PeerCertificates[0]
		report.CertificateSubject = certificate.Subject.String()
		report.CertificateIssuer = certificate.Issuer.String()
		report.CertificateExpiresAt = certificate.NotAfter.UTC().Format(time.RFC3339)
	}
	return report
}
