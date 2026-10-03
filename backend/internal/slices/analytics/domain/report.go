package domain

import (
	"math"
	"time"
)

// TokenVolume is the billable shape of what flowed through one dimension
// (a provider, a model, a day). The raw counts come straight from the
// persisted history; the priced ones are estimates and say so.
type TokenVolume struct {
	Requests       int   `json:"requests"`
	Completed      int   `json:"completed"`
	Failed         int   `json:"failed"`
	Cancelled      int   `json:"cancelled"`
	Retries        int   `json:"retries"`
	InputTokens    int64 `json:"inputTokens"`
	OutputTokens   int64 `json:"outputTokens"`
	CachedTokens   int64 `json:"cachedTokens"`
	ReasoningToken int64 `json:"reasoningTokens"`
	TotalTokens    int64 `json:"totalTokens"`
	// GenerationMS is the summed generation time of completed requests; the
	// tokens-per-second figure is derived from it, exactly as the history
	// slice computes it, so the two workspaces agree.
	GenerationMS float64 `json:"generationMs"`
	// Cost is set only when every token kind this row consumed has a price;
	// an unpriced row keeps Cost zero and IsPriced false so the gap is
	// visible rather than silently understated.
	Cost     float64 `json:"cost"`
	IsPriced bool    `json:"isPriced"`
}

// TokensPerSecond is output tokens per second of generation, or 0 when
// nothing was generated in the period.
func (volume TokenVolume) TokensPerSecond() float64 {
	if volume.GenerationMS <= 0 {
		return 0
	}
	return float64(volume.OutputTokens) * 1000 / volume.GenerationMS
}

// SuccessRate is completed requests over all requests, 0..1, with zero
// requests reading as 0 rather than NaN: an empty period is not a perfect one.
func (volume TokenVolume) SuccessRate() float64 {
	if volume.Requests == 0 {
		return 0
	}
	return float64(volume.Completed) / float64(volume.Requests)
}

// ProviderStats is one provider's measured behaviour in the requested period.
// Latency percentiles are computed from completed requests only: a failed
// request has no latency worth trusting.
type ProviderStats struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Volume          TokenVolume  `json:"volume"`
	P50MS           float64      `json:"p50Ms"`
	P95MS           float64      `json:"p95Ms"`
	AvgMS           float64      `json:"avgMs"`
	TokensPerSecond float64      `json:"tokensPerSecond"`
	Errors          []ErrorCount `json:"errors"`
}

// ModelStats is one model's measured behaviour across every provider that
// served it. ProviderName carries the last provider seen serving it, which
// is enough to explain where a model's numbers came from.
type ModelStats struct {
	Model           string       `json:"model"`
	ProviderName    string       `json:"providerName"`
	Volume          TokenVolume  `json:"volume"`
	P50MS           float64      `json:"p50Ms"`
	P95MS           float64      `json:"p95Ms"`
	AvgMS           float64      `json:"avgMs"`
	TokensPerSecond float64      `json:"tokensPerSecond"`
	Errors          []ErrorCount `json:"errors"`
}

// DailyPoint is one local day of traffic. Cost is the sum of per-request
// estimates, so it inherits their honesty: unpriced requests contribute
// nothing and are counted in UnpricedRequests instead.
type DailyPoint struct {
	Date            string      `json:"date"`
	Volume          TokenVolume `json:"volume"`
	SuccessRate     float64     `json:"successRate"`
	TokensPerSecond float64     `json:"tokensPerSecond"`
}

// ErrorCount names a failure class the way history recorded it. ErrorCode is
// the relay's own stable classification, never the provider's prose.
type ErrorCount struct {
	ErrorCode string `json:"errorCode"`
	Requests  int    `json:"requests"`
}

// Overview aggregates the whole period into the numbers a switchboard owner
// asks daily: how much, how reliably, how fast, at what estimated cost.
type Overview struct {
	Volume          TokenVolume `json:"volume"`
	SuccessRate     float64     `json:"successRate"`
	P50MS           float64     `json:"p50Ms"`
	P95MS           float64     `json:"p95Ms"`
	TokensPerSecond float64     `json:"tokensPerSecond"`
	// PricedRequests is how many completed requests the cost estimate is
	// actually built from; the difference from Completed is the unpriced
	// remainder, shown rather than folded away.
	PricedRequests int    `json:"pricedRequests"`
	TopErrorCode   string `json:"topErrorCode"`
}

// Report is one period's answer, assembled from the facts store and the
// price catalog. Lists are bounded and sorted: providers and models by
// total tokens, errors by count.
type Report struct {
	Period      string    `json:"period"`
	GeneratedAt time.Time `json:"generatedAt"`
	// Currency is the catalog's unit of account; cost figures read in it.
	Currency  string          `json:"currency"`
	Overview  Overview        `json:"overview"`
	Providers []ProviderStats `json:"providers"`
	Models    []ModelStats    `json:"models"`
	Daily     []DailyPoint    `json:"daily"`
	Errors    []ErrorCount    `json:"errors"`
	// UnpricedModels names the models the cost estimate does not cover, so
	// "what am I missing" is a list, not a guess.
	UnpricedModels []string `json:"unpricedModels"`
}

// LatencySample is one completed request's latency attributed to its
// dimensions; the facts store emits them and the application turns them into
// percentiles.
type LatencySample struct {
	ProviderID string  `json:"providerId"`
	Model      string  `json:"model"`
	LatencyMS  float64 `json:"latencyMs"`
}

// GroupedRow is what SQL can aggregate honestly: sums and counts per
// (provider, model). Anything percentile-shaped is built from samples.
type GroupedRow struct {
	ProviderID   string `json:"providerId"`
	ProviderName string `json:"providerName"`
	Model        string `json:"model"`
	TokenVolume
}

// DailyRow is one local day's sums for one model, as grouped by the facts
// store. The application folds models into days so each model's tokens are
// priced at its own rate before the day total exists.
type DailyRow struct {
	Date  string `json:"date"`
	Model string `json:"model"`
	TokenVolume
}

// ErrorRow is one (dimension, error code) count.
type ErrorRow struct {
	ProviderID string `json:"providerId"`
	Model      string `json:"model"`
	ErrorCode  string `json:"errorCode"`
	Requests   int    `json:"requests"`
}

// PercentileMS returns the nearest-rank percentile of sorted latencies.
// An empty slice returns 0: "no measurement" must not masquerade as a fast
// one. Nearest-rank (ceil(p*n)-th) is the definition SQLite's OFFSET trick
// already used elsewhere in this codebase, so numbers agree across slices.
func PercentileMS(sorted []float64, percentile float64) float64 {
	if len(sorted) == 0 || percentile <= 0 || percentile >= 1 {
		return 0
	}
	rank := int(math.Ceil(percentile * float64(len(sorted))))
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
