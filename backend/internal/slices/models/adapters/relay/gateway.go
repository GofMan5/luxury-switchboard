package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

const (
	maxDiscoveredModels  = 5_000
	maxShortProbeTimeout = 30 * time.Second
	maxFullProbeTimeout  = 5 * time.Minute
	// The Responses API refuses a budget under 16 tokens, so a smaller probe
	// would report every reasoning model as unavailable.
	shortProbeTokens = 16
	fullProbeTokens  = 128
	// Compatibility names used by the existing short-probe tests.
	maxProbeTimeout = maxShortProbeTimeout
	probeTokens     = shortProbeTokens
)

type Gateway struct {
	dispatcher relayapp.Dispatcher
	timeout    atomic.Int64
}

func NewGateway(dispatcher relayapp.Dispatcher, timeout time.Duration) *Gateway {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	gateway := &Gateway{dispatcher: dispatcher}
	gateway.timeout.Store(int64(timeout))
	return gateway
}

// SetTimeout moves the discovery/probe budget live: the probe cap follows the
// new budget on the next call.
func (gateway *Gateway) SetTimeout(timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	gateway.timeout.Store(int64(timeout))
}

func (gateway *Gateway) probeBudget() time.Duration {
	return gateway.probeBudgetFor(domain.TestProfileShort)
}

func (gateway *Gateway) probeBudgetFor(profile domain.TestProfile) time.Duration {
	cap := maxShortProbeTimeout
	if profile == domain.TestProfileFull128K {
		cap = maxFullProbeTimeout
	}
	return min(time.Duration(gateway.timeout.Load()), cap)
}

func relayProfile(profile domain.TestProfile) relayapp.ProbeProfile {
	if profile == domain.TestProfileFull128K {
		return relayapp.ProbeProfileFull128K
	}
	return relayapp.ProbeProfileShort
}

func (gateway *Gateway) Discover(ctx context.Context, provider domain.Provider) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(gateway.timeout.Load()))
	defer cancel()
	response, err := gateway.dispatcher.Dispatch(ctx, relayapp.DispatchRequest{
		Method: http.MethodGet, Path: provider.ModelsPath, ProviderID: provider.ID,
		UpstreamModel: "__model_catalog__", AttemptLimit: 1,
		UseStoredCredential: true,
		Headers:             http.Header{"Accept": []string{"application/json"}},
	})
	if err != nil {
		return nil, errors.New("model catalog is unavailable")
	}
	// A refused credential is the one cause fixed in API Keys, so it keeps its own
	// error instead of arriving as the same sentence as a wrong discovery path.
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
		return nil, application.ErrDiscoveryUnauthorized
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, errors.New("model catalog is unavailable")
	}
	return extractModelIDs(response.Body)
}

func (gateway *Gateway) Test(ctx context.Context, provider domain.Provider, model string) domain.TestResult {
	return gateway.TestWithProfile(ctx, provider, model, domain.TestProfileShort)
}

func (gateway *Gateway) TestWithProfile(ctx context.Context, provider domain.Provider, model string, profile domain.TestProfile) domain.TestResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, gateway.probeBudgetFor(profile))
	defer cancel()
	// The streaming probe answers everything the plain request does and more:
	// the first-token time and the token count. A dispatcher without it (a
	// test double) takes the buffered path.
	if prober, ok := gateway.dispatcher.(relayapp.ProfiledStreamProber); ok {
		report, err := prober.ProbeWithProfile(ctx, provider.ID, model, relayProfile(profile))
		return gateway.resultFromReport(ctx, provider.ID, model, report, err)
	}
	if profile == domain.TestProfileShort {
		if prober, ok := gateway.dispatcher.(relayapp.StreamProber); ok {
			report, err := prober.Probe(ctx, provider.ID, model)
			return gateway.resultFromReport(ctx, provider.ID, model, report, err)
		}
	}
	response, err := gateway.testRequest(ctx, provider, model, profile)
	result := domain.TestResult{ProviderID: provider.ID, Model: model, LatencyMS: float64(time.Since(started).Microseconds()) / 1000}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.State = "timeout"
			result.ErrorCode = "timeout"
		} else {
			result.State = "unavailable"
			result.ErrorCode = "transport"
		}
		return result
	}
	result.Status = response.Status
	if response.Status >= 200 && response.Status < 300 {
		if failedResponse(response.Body) {
			result.State = "unavailable"
			result.ErrorCode = "request_rejected"
			return result
		}
		result.State = "available"
		return result
	}
	result.State = "unavailable"
	result.ErrorCode = statusCode(response.Status)
	return result
}

func (gateway *Gateway) resultFromReport(ctx context.Context, providerID, model string, report relayapp.ProbeReport, err error) domain.TestResult {
	result := domain.TestResult{
		ProviderID: providerID, Model: model,
		Status: report.Status, LatencyMS: report.TotalMs,
		TTFTMS: report.TTFTMs, OutputTokens: report.OutputTokens,
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.State = "timeout"
			result.ErrorCode = "timeout"
		} else {
			result.State = "unavailable"
			result.ErrorCode = "transport"
		}
		return result
	}
	if report.Status >= 200 && report.Status < 300 && report.ErrorCode == "" {
		result.State = "available"
		return result
	}
	result.State = "unavailable"
	if report.ErrorCode != "" {
		result.ErrorCode = report.ErrorCode
	} else {
		result.ErrorCode = statusCode(report.Status)
	}
	return result
}

func failedResponse(body []byte) bool {
	var payload map[string]any
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return false
	}
	for _, candidate := range []map[string]any{payload, mapValue(payload["response"])} {
		status, _ := candidate["status"].(string)
		eventType, _ := candidate["type"].(string)
		if status == "failed" || status == "cancelled" || eventType == "error" || eventType == "response.failed" || candidate["error"] != nil {
			return true
		}
	}
	return false
}

func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func (gateway *Gateway) testRequest(ctx context.Context, provider domain.Provider, model string, profile domain.TestProfile) (relayapp.DispatchResponse, error) {
	outputTokens := shortProbeTokens
	prompt := relayProfile(profile).Prompt()
	if profile == domain.TestProfileFull128K {
		outputTokens = fullProbeTokens
	}
	if provider.Dialect == "anthropic" {
		return gateway.dispatch(ctx, provider.ID, model, "/v1/messages", map[string]any{
			"model": model, "max_tokens": outputTokens,
			"messages": []map[string]string{{"role": "user", "content": prompt}},
		})
	}
	response, err := gateway.dispatch(ctx, provider.ID, model, "/v1/responses", map[string]any{
		"model": model, "input": prompt, "max_output_tokens": outputTokens,
	})
	if err == nil && !responsesFallback(response) {
		return response, nil
	}
	return gateway.dispatch(ctx, provider.ID, model, "/v1/chat/completions", map[string]any{
		"model": model, "max_tokens": outputTokens,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
}

func responsesFallback(response relayapp.DispatchResponse) bool {
	if response.Status >= 200 && response.Status < 300 && failedResponse(response.Body) {
		return true
	}
	switch response.Status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func (gateway *Gateway) dispatch(ctx context.Context, providerID, model, path string, payload any) (relayapp.DispatchResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return relayapp.DispatchResponse{}, err
	}
	return gateway.dispatcher.Dispatch(ctx, relayapp.DispatchRequest{
		Method: http.MethodPost, Path: path, ProviderID: providerID,
		PublicModel: model, UpstreamModel: model, Body: body, AttemptLimit: 1,
		UseStoredCredential: true,
		Headers:             http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json"}},
	})
}

func extractModelIDs(body []byte) ([]string, error) {
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("invalid model catalog")
	}
	var values []any
	switch root := payload.(type) {
	case map[string]any:
		values, _ = root["data"].([]any)
		if values == nil {
			values, _ = root["models"].([]any)
		}
	case []any:
		values = root
	}
	if values == nil {
		return nil, errors.New("model catalog is missing models")
	}
	seen := make(map[string]struct{}, len(values))
	models := make([]string, 0, len(values))
	for _, value := range values {
		model := ""
		switch item := value.(type) {
		case string:
			model = item
		case map[string]any:
			for _, key := range []string{"id", "model", "name"} {
				if candidate, valid := item[key].(string); valid {
					model = candidate
					break
				}
			}
		}
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 128 || strings.ContainsAny(model, "\r\n\x00") {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		if len(models) == maxDiscoveredModels {
			return nil, errors.New("model catalog exceeds the safety limit")
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, errors.New("model catalog is empty")
	}
	sort.Strings(models)
	return models, nil
}

func statusCode(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "authentication"
	case http.StatusNotFound:
		return "model_unavailable"
	case http.StatusTooManyRequests:
		return "rate_limited"
	default:
		if status >= 500 {
			return "provider_unavailable"
		}
		return "request_rejected"
	}
}
