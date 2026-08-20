package application

import (
	"errors"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
)

// Inspector applies the guardrails to finished provider answers and keeps a
// bounded record of what it saw. It is safe for concurrent use: the relay calls
// it from every in-flight request.
type Inspector struct {
	engine *domain.Engine

	mu       sync.RWMutex
	mode     domain.Mode
	records  []Record
	capacity int
	sequence uint64

	listenersMu sync.RWMutex
	listeners   []func(Record)

	now func() time.Time
}

// Record is one inspected answer, kept for the operator. It holds rule metadata
// and bounded excerpts only — never the prompt, the answer or a credential.
type Record struct {
	ID           string           `json:"id"`
	At           time.Time        `json:"at"`
	Verdict      domain.Verdict   `json:"verdict"`
	Severity     string           `json:"severity"`
	ProviderID   string           `json:"providerId"`
	ProviderName string           `json:"providerName"`
	Model        string           `json:"model"`
	Findings     []domain.Finding `json:"findings"`
}

// Subject is the safe context of the answer being inspected. It carries
// identifiers, never content.
type Subject struct {
	ProviderID   string
	ProviderName string
	Model        string
	// ClientDeclaredTools says whether the request offered the model any tool.
	ClientDeclaredTools bool
}

// Decision is the outcome for one answer.
type Decision struct {
	Verdict  domain.Verdict
	Severity domain.Severity
	Findings []domain.Finding
}

// Blocked reports whether the answer must not reach the client.
func (decision Decision) Blocked() bool { return decision.Verdict == domain.VerdictBlocked }

const defaultCapacity = 500

// NewInspector builds an inspector around a compiled engine.
func NewInspector(engine *domain.Engine, mode domain.Mode, capacity int) (*Inspector, error) {
	if engine == nil {
		return nil, errors.New("guardrail engine is required")
	}
	if _, err := domain.ParseMode(string(mode)); err != nil {
		return nil, err
	}
	if capacity < 1 {
		capacity = defaultCapacity
	}
	return &Inspector{engine: engine, mode: mode, capacity: capacity, now: time.Now}, nil
}

// Mode reports the active mode.
func (inspector *Inspector) Mode() domain.Mode {
	inspector.mu.RLock()
	defer inspector.mu.RUnlock()
	return inspector.mode
}

// SetMode switches the mode at runtime. Settings changes must take effect without
// a restart, and an in-flight request keeps whichever mode it started with.
func (inspector *Inspector) SetMode(mode domain.Mode) error {
	if _, err := domain.ParseMode(string(mode)); err != nil {
		return err
	}
	inspector.mu.Lock()
	inspector.mode = mode
	inspector.mu.Unlock()
	return nil
}

// RuleCount reports how many rules are loaded, for the operator's confidence.
func (inspector *Inspector) RuleCount() int { return inspector.engine.RuleCount() }

// IndicatorCount reports how many literal indicators are loaded.
func (inspector *Inspector) IndicatorCount() int { return inspector.engine.IndicatorCount() }

// RuleSetVersion reports the vendored rule set version.
func (inspector *Inspector) RuleSetVersion() int { return inspector.engine.Version() }

// Inspect judges one finished answer. In ModeOff it does no work at all — not
// even extraction — so a user who turns the guardrails off pays nothing.
func (inspector *Inspector) Inspect(body []byte, eventStream bool, subject Subject) Decision {
	mode := inspector.Mode()
	if !mode.Inspects() || len(body) == 0 {
		return Decision{Verdict: domain.VerdictClean}
	}
	extraction := Extract(body, eventStream)
	findings := make([]domain.Finding, 0, 8)
	for _, piece := range extraction.Pieces {
		findings = append(findings, inspector.engine.Inspect(piece.Text, piece.Source)...)
	}
	// A tool call the client cannot have asked for is an anomaly no pattern can
	// see: it needs both halves of the exchange.
	if !subject.ClientDeclaredTools {
		for _, name := range extraction.ToolNames {
			findings = append(findings, domain.UnsolicitedToolFinding(name))
		}
	}
	// Sort BEFORE capping. Capping first would make the cap a first-come limit, and
	// a provider could bury the one high-severity finding behind enough low-severity
	// noise to push it out of the list — which in block mode is the whole decision.
	domain.SortFindings(findings)
	if len(findings) > domain.MaxFindings {
		findings = findings[:domain.MaxFindings]
	}
	decision := Decision{
		Verdict:  mode.Decide(findings),
		Severity: domain.MaxSeverity(findings),
		Findings: findings,
	}
	if decision.Verdict != domain.VerdictClean {
		inspector.record(decision, subject)
	}
	return decision
}

// Records returns the most recent findings, newest first.
func (inspector *Inspector) Records(limit int) []Record {
	inspector.mu.RLock()
	defer inspector.mu.RUnlock()
	if limit < 1 || limit > len(inspector.records) {
		limit = len(inspector.records)
	}
	result := make([]Record, limit)
	copy(result, inspector.records[:limit])
	return result
}

// Clear drops the recorded findings.
func (inspector *Inspector) Clear() {
	inspector.mu.Lock()
	inspector.records = nil
	inspector.mu.Unlock()
}

// OnRecord registers a listener for new records so the UI can follow them live.
func (inspector *Inspector) OnRecord(listener func(Record)) {
	if listener == nil {
		return
	}
	inspector.listenersMu.Lock()
	inspector.listeners = append(inspector.listeners, listener)
	inspector.listenersMu.Unlock()
}

func (inspector *Inspector) record(decision Decision, subject Subject) {
	inspector.mu.Lock()
	inspector.sequence++
	record := Record{
		ID:           "gr_" + formatUint(inspector.sequence),
		At:           inspector.now().UTC(),
		Verdict:      decision.Verdict,
		Severity:     decision.Severity.String(),
		ProviderID:   truncate(subject.ProviderID, 64),
		ProviderName: truncate(subject.ProviderName, 80),
		Model:        truncate(subject.Model, 128),
		Findings:     decision.Findings,
	}
	inspector.records = append([]Record{record}, inspector.records...)
	if len(inspector.records) > inspector.capacity {
		inspector.records = inspector.records[:inspector.capacity]
	}
	inspector.mu.Unlock()

	inspector.listenersMu.RLock()
	listeners := inspector.listeners
	inspector.listenersMu.RUnlock()
	for _, listener := range listeners {
		listener(record)
	}
}

func formatUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
