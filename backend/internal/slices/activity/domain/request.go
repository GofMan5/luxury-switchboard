package domain

import "time"

type State string

// MaxTags bounds the labels one request carries.
const MaxTags = 16

const (
	StateActive    State = "active"
	StateRetrying  State = "retrying"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// IsTerminal reports whether a request reached a final state and will not
// change again. Memory trimming and history persistence share it so a state
// added here cannot leak into one path and not the other.
func IsTerminal(state State) bool {
	return state == StateCompleted || state == StateFailed || state == StateCancelled
}

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
	ErrorDetail     string    `json:"errorDetail,omitempty"`
	InputTokens     int64     `json:"inputTokens"`
	OutputTokens    int64     `json:"outputTokens"`
	CachedTokens    int64     `json:"cachedTokens"`
	ReasoningTokens int64     `json:"reasoningTokens"`
	TotalTokens     int64     `json:"totalTokens"`
	ContextTokens   int64     `json:"contextTokens"`
	GenerationMS    float64   `json:"generationMs"`
	TokensPerSecond float64   `json:"tokensPerSecond"`
	// TTFTMS is the time from the request's start to the first generated
	// token (answer or reasoning) of the serving attempt; zero when none
	// streamed.
	TTFTMS float64 `json:"ttftMs,omitempty"`
	// ReasoningEffort is the reasoning level the client asked for, one of
	// the allowlisted names; ReasoningBudget is its thinking-token budget
	// when the request set one. Both are request parameters, never content.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ReasoningBudget int64  `json:"reasoningBudget,omitempty"`
	// Thinking is true while the provider's stream is in its reasoning
	// phase; ThinkingMS accumulates the closed reasoning spans.
	Thinking   bool    `json:"thinking,omitempty"`
	ThinkingMS float64 `json:"thinkingMs,omitempty"`
	// Tags are the operator's labels from request rules and relay hooks:
	// at most MaxTags, each ^[a-z0-9][a-z0-9_.-]{0,31}$, in first-seen order.
	Tags []string `json:"tags,omitempty"`
	// ThinkingSince opens the current reasoning span. Internal: the span's
	// length lands in ThinkingMS when it closes.
	ThinkingSince time.Time `json:"-"`
}

type Start struct {
	Model           string
	ProviderID      string
	ProviderName    string
	Method          string
	Path            string
	BytesIn         int64
	ReasoningEffort string
	ReasoningBudget int64
	Tags            []string
}

type Retry struct {
	Attempt int
	Status  int
	Delay   time.Duration
}

type Finish struct {
	ProviderID      string
	ProviderName    string
	Status          int
	BytesOut        int64
	Cancelled       bool
	ErrorCode       string
	ErrorDetail     string
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	TotalTokens     int64
	ContextTokens   int64
	Generation      time.Duration
	// FirstToken is measured from the request's start; TokensPerSecond is
	// the serving attempt's decode rate, already guarded by the relay.
	FirstToken      time.Duration
	TokensPerSecond float64
	// Tags join the request's start tags; hooks add theirs after the start.
	Tags []string
}

type Summary struct {
	Requests    int     `json:"requests"`
	Active      int     `json:"active"`
	Queued      int     `json:"queued"`
	SuccessRate float64 `json:"successRate"`
	P95MS       float64 `json:"p95Ms"`
	RPM         float64 `json:"rpm"`
}
