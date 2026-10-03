package domain

type Provider struct {
	ID         string
	ModelsPath string
	Dialect    string
}

type TestResult struct {
	RunID      string  `json:"runId"`
	ProviderID string  `json:"providerId"`
	Model      string  `json:"model"`
	State      string  `json:"state"`
	Status     int     `json:"status"`
	LatencyMS  float64 `json:"latencyMs"`
	// TTFTMS is the time to the first content token of a streaming probe; zero
	// on a refused or non-streaming answer. OutputTokens is the provider's own
	// count, or the number of content frames when the provider never reports.
	TTFTMS       float64 `json:"ttftMs,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	ErrorCode    string  `json:"errorCode,omitempty"`
}
