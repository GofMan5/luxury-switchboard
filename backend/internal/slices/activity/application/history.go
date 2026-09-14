package application

import (
	"context"
	"math"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

type Period string

const (
	Period24H Period = "24h"
	Period48H Period = "48h"
	Period72H Period = "72h"
	PeriodAll Period = "all"
)

type HistoryStats struct {
	Requests        int64   `json:"requests"`
	Completed       int64   `json:"completed"`
	Failed          int64   `json:"failed"`
	Cancelled       int64   `json:"cancelled"`
	Retries         int64   `json:"retries"`
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CachedTokens    int64   `json:"cachedTokens"`
	ReasoningTokens int64   `json:"reasoningTokens"`
	ProcessedTokens int64   `json:"processedTokens"`
	NonCachedTokens int64   `json:"nonCachedTokens"`
	P95MS           float64 `json:"p95Ms"`
	TokensPerSecond float64 `json:"tokensPerSecond"`
}

type History interface {
	Record(domain.Request) bool
	Recent(context.Context, Period, int) ([]domain.Request, error)
	Stats(context.Context, Period) (HistoryStats, error)
	Close(context.Context) error
}

// ValidPeriod reports whether period names a known history window. Stdio
// handlers check it first so an unknown period reads as invalid_payload
// instead of a storage failure.
func ValidPeriod(period Period) bool {
	switch period {
	case Period24H, Period48H, Period72H, PeriodAll:
		return true
	default:
		return false
	}
}

// P95Index is the 0-based rank of the p95 element among count sorted
// latencies. In-memory summaries and the history percentile query share it
// so both read the same element.
func P95Index(count int) int {
	if count < 1 {
		return 0
	}
	return max(int(math.Ceil(float64(count)*0.95))-1, 0)
}
