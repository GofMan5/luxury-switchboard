package application

import (
	"context"
	"net/http"
	"strings"
)

type DispatchRequest struct {
	Method              string
	Path                string
	Headers             http.Header
	Body                []byte
	ProviderID          string
	PublicModel         string
	UpstreamModel       string
	AttemptLimit        int
	UseStoredCredential bool
}

type DispatchResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
	// Which terminal event the upstream stream ended on, or "" for a clean one.
	// No caller reads it: the tunnel decides for itself whether an answer is safe
	// to forward, and it looks at the body rather than trusting this. It survives
	// because it is the only place the decision is still visible from outside —
	// the header it comes from is deleted before the response leaves Dispatch —
	// and three tests observe the retry ladder through it. Deleting it as dead
	// would blind them without failing anything.
	Terminal         string
	SensitiveMarkers []string `json:"-"`
}

type Dispatcher interface {
	Dispatch(context.Context, DispatchRequest) (DispatchResponse, error)
}

// ProbeReport is one measured streaming request: when the first content token
// arrived, when the answer finished, and how many tokens the provider reported
// (or the probe counted). OutputTPS is the decode rate (domain.DecodeRate),
// zero when the stream carried too little evidence to measure one. ErrorCode
// carries the failure class like a history row does.
type ProbeReport struct {
	Status       int     `json:"status"`
	TTFTMs       float64 `json:"ttftMs"`
	TotalMs      float64 `json:"totalMs"`
	OutputTokens int     `json:"outputTokens"`
	OutputTPS    float64 `json:"outputTps"`
	ErrorCode    string  `json:"errorCode"`
	ErrorDetail  string  `json:"errorDetail"`
}

// StreamProber measures a provider+model with a real streaming request. The
// buffered Dispatch cannot answer "when did the first token arrive" because it
// sees the answer whole; the probe reads the stream as it arrives.
type StreamProber interface {
	Probe(ctx context.Context, providerID, model string) (ProbeReport, error)
}

// ProbeProfile is deliberately small: model testing has two operator-facing
// budgets, and keeping them typed prevents arbitrary request sizes crossing the
// control-plane boundary.
type ProbeProfile string

const (
	ProbeProfileShort    ProbeProfile = "short"
	ProbeProfileFull128K ProbeProfile = "full-128k"
)

func (profile ProbeProfile) Valid() bool {
	return profile == ProbeProfileShort || profile == ProbeProfileFull128K
}

// probeTask asks for a steady run of short tokens: enough of them, in enough
// chunks, for a decode rate to mean something — a two-token "OK" measures the
// network, not the model. Sixty numbers stay inside the probe's output budget
// even for tokenizers that split every digit (about 170 tokens), so the
// answer ends on its own instead of hitting the cap, which the Responses
// dialect reports as an incomplete — failed — answer.
const probeTask = "Count from 1 to 60, separated by single spaces. Output only the numbers."

// The shared input leaves headroom for output and protocol framing. Token counts
// vary by provider tokenizer; this probes approximately 128k, not an exact limit.
var fullProbeInput = strings.Repeat("context ", 127_500) + "\n" + probeTask

func (profile ProbeProfile) Prompt() string {
	if profile == ProbeProfileFull128K {
		return fullProbeInput
	}
	return probeTask
}

// ProfiledStreamProber is optional so older test doubles and sidecars keep the
// short-probe contract while the live relay can opt into the larger profile.
type ProfiledStreamProber interface {
	ProbeWithProfile(ctx context.Context, providerID, model string, profile ProbeProfile) (ProbeReport, error)
}
