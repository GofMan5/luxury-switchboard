package domain

type Provider struct {
	ID         string
	ModelsPath string
	Dialect    string
}

// TestProfile is the only sizing choice exposed by model tests. Short keeps
// routine checks cheap; Full128K deliberately exercises a large context so an
// operator can distinguish reachability from context handling.
type TestProfile string

const (
	TestProfileShort    TestProfile = "short"
	TestProfileFull128K TestProfile = "full-128k"
)

func (profile TestProfile) Valid() bool {
	return profile == TestProfileShort || profile == TestProfileFull128K
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
	// OutputTPS is the decode rate — tokens after the first over the window
	// from the first token to the end — and zero when the stream was too
	// short or too bursty to measure one.
	TTFTMS       float64 `json:"ttftMs,omitempty"`
	OutputTokens int     `json:"outputTokens,omitempty"`
	OutputTPS    float64 `json:"outputTps,omitempty"`
	ErrorCode    string  `json:"errorCode,omitempty"`
}
