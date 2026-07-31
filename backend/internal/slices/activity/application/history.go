package application

import (
	"context"

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
