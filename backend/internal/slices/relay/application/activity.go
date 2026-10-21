package application

import "time"

type ActivityStart struct {
	Model        string
	ProviderID   string
	ProviderName string
	Method       string
	Path         string
	BytesIn      int64
	// The reasoning level the client requested: the effort name, and the
	// thinking-token budget where the dialect speaks in budgets instead.
	ReasoningEffort string
	ReasoningBudget int64
}

type ActivityRetry struct {
	Attempt int
	Status  int
	Delay   time.Duration
}

type ActivityFinish struct {
	// The final serving provider can differ from Begin after local failover.
	ProviderID   string
	ProviderName string
	Status       int
	BytesOut     int64
	Cancelled    bool
	ErrorCode    string
	ErrorDetail  string
	Usage        TokenUsage
	Generation   time.Duration
	// Pace is the serving attempt's streaming measurement; its zero value
	// says the answer carried none.
	Pace StreamPace
}

// StreamPace is one streamed answer's timing: when its first generated token
// (answer or reasoning) arrived, measured from the request's start, and its
// decode rate (domain.DecodeRate). The rate is zero unless the stream carried
// enough evidence to make it a speed rather than a buffered burst.
type StreamPace struct {
	FirstToken      time.Duration
	TokensPerSecond float64
}

type TokenUsage struct {
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	TotalTokens     int64
	ContextTokens   int64
}

type ActivitySink interface {
	Begin(ActivityStart) string
	Waiting(string)
	Resume(string, time.Duration)
	Retry(string, ActivityRetry)
	// Thinking reports the stream's phase: true while the provider streams
	// reasoning, false once the answer itself starts.
	Thinking(string, bool)
	Finish(string, ActivityFinish)
}

type NoopActivity struct{}

func (NoopActivity) Begin(ActivityStart) string    { return "" }
func (NoopActivity) Waiting(string)                {}
func (NoopActivity) Resume(string, time.Duration)  {}
func (NoopActivity) Retry(string, ActivityRetry)   {}
func (NoopActivity) Thinking(string, bool)         {}
func (NoopActivity) Finish(string, ActivityFinish) {}
