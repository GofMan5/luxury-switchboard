package application

import "time"

type ActivityStart struct {
	Model        string
	ProviderID   string
	ProviderName string
	Method       string
	Path         string
	BytesIn      int64
}

type ActivityRetry struct {
	Attempt int
	Status  int
	Delay   time.Duration
}

type ActivityFinish struct {
	Status     int
	BytesOut   int64
	Cancelled  bool
	ErrorCode  string
	Usage      TokenUsage
	Generation time.Duration
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
	Finish(string, ActivityFinish)
}

type NoopActivity struct{}

func (NoopActivity) Begin(ActivityStart) string    { return "" }
func (NoopActivity) Waiting(string)                {}
func (NoopActivity) Resume(string, time.Duration)  {}
func (NoopActivity) Retry(string, ActivityRetry)   {}
func (NoopActivity) Finish(string, ActivityFinish) {}
