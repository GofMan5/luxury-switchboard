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
	ErrorCode  string  `json:"errorCode,omitempty"`
}
