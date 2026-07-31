package domain

import "time"

type State string

const (
	StateActive    State = "active"
	StateRetrying  State = "retrying"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type Request struct {
	ID              string    `json:"id"`
	StartedAt       time.Time `json:"startedAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	State           State     `json:"state"`
	Model           string    `json:"model"`
	ProviderID      string    `json:"providerId"`
	ProviderName    string    `json:"providerName"`
	Method          string    `json:"method"`
	Path            string    `json:"path"`
	Status          int       `json:"status,omitempty"`
	QueueMS         float64   `json:"queueMs"`
	LatencyMS       float64   `json:"latencyMs"`
	Retries         int       `json:"retries"`
	BytesIn         int64     `json:"bytesIn"`
	BytesOut        int64     `json:"bytesOut"`
	ErrorCode       string    `json:"errorCode,omitempty"`
	InputTokens     int64     `json:"inputTokens"`
	OutputTokens    int64     `json:"outputTokens"`
	CachedTokens    int64     `json:"cachedTokens"`
	ReasoningTokens int64     `json:"reasoningTokens"`
	TotalTokens     int64     `json:"totalTokens"`
	ContextTokens   int64     `json:"contextTokens"`
	GenerationMS    float64   `json:"generationMs"`
	TokensPerSecond float64   `json:"tokensPerSecond"`
}

type Start struct {
	Model        string
	ProviderID   string
	ProviderName string
	Method       string
	Path         string
	BytesIn      int64
}

type Retry struct {
	Attempt int
	Status  int
	Delay   time.Duration
}

type Finish struct {
	Status          int
	BytesOut        int64
	Cancelled       bool
	ErrorCode       string
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	TotalTokens     int64
	ContextTokens   int64
	Generation      time.Duration
}

type Summary struct {
	Requests    int     `json:"requests"`
	Active      int     `json:"active"`
	Queued      int     `json:"queued"`
	SuccessRate float64 `json:"successRate"`
	P95MS       float64 `json:"p95Ms"`
	RPM         float64 `json:"rpm"`
}
