package application

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
)

// Inspector applies the guardrails to finished provider answers and keeps a
// bounded record of what it saw. It is safe for concurrent use: the relay calls
// it from every in-flight request.
type Inspector struct {
	engine *domain.Engine

	mu   sync.RWMutex
	mode domain.Mode
	// providerModes overrides the inspection mode per provider: distrust
	// earned by one reseller does not have to be served to every other. A
	// global off wins over everything.
	providerModes map[string]domain.Mode
	records       []Record
	capacity      int
	sequence      uint64

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
	// Occurrences is how many answers this record stands for. It is 1 for a real
	// detection — those are never folded together, because two identical payloads
	// from a provider are two attempts and the operator must see both.
	Occurrences int `json:"occurrences"`
}

// Subject is the safe context of the answer being inspected. It carries
// identifiers, never content.
type Subject struct {
	ProviderID   string
	ProviderName string
	Model        string
	// ClientDeclaredTools says whether the request offered the model any tool.
	ClientDeclaredTools bool
	// Secrets are the markers of the credential in flight: a provider that
	// echoes the key it was sent back inside its answer must not have that key
	// recorded as evidence. The findings page shows excerpts of the answer,
	// and an excerpt is the answer.
	Secrets []string
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

// SetCapacity moves the findings bound live; a smaller bound drops the oldest
// records right away instead of waiting for the next finding to trim them.
func (inspector *Inspector) SetCapacity(capacity int) {
	if capacity < 1 {
		capacity = defaultCapacity
	}
	inspector.mu.Lock()
	inspector.capacity = capacity
	if len(inspector.records) > inspector.capacity {
		inspector.records = inspector.records[:inspector.capacity]
	}
	inspector.mu.Unlock()
}

// SetMode switches the mode at runtime. Settings changes must take effect without
// a restart, and an in-flight request keeps whichever mode it started with.
func (inspector *Inspector) SetMode(mode domain.Mode) error {
	if _, err := domain.ParseMode(string(mode)); err != nil {
		return err
	}
	inspector.mu.Lock()
	inspector.mode = mode
	// "Off" is a global statement: it disables inspection everywhere, and a
	// provider table that would quietly re-enable it under one provider is a
	// settings file that lies about what it does.
	if mode == domain.ModeOff {
		inspector.providerModes = map[string]domain.Mode{}
	}
	inspector.mu.Unlock()
	return nil
}

// SetProviderModes replaces the whole per-provider override table at once. The
// settings slice owns the table; the inspector only enforces what it says.
// Empty and unknown values read as "follow the global mode".
func (inspector *Inspector) SetProviderModes(modes map[string]string) {
	parsed := make(map[string]domain.Mode, len(modes))
	for provider, mode := range modes {
		if value, err := domain.ParseMode(mode); err == nil && value != domain.ModeOff {
			parsed[provider] = value
		}
	}
	inspector.mu.Lock()
	inspector.providerModes = parsed
	if inspector.mode == domain.ModeOff {
		inspector.providerModes = map[string]domain.Mode{}
	}
	inspector.mu.Unlock()
}

// Mode reports the global default. The Guardrails page shows it; requests
// resolve through ModeFor.
func (inspector *Inspector) Mode() domain.Mode {
	inspector.mu.RLock()
	defer inspector.mu.RUnlock()
	return inspector.mode
}

// ProviderModes reports the live override table, for the status the settings
// page reads back.
func (inspector *Inspector) ProviderModes() map[string]string {
	inspector.mu.RLock()
	defer inspector.mu.RUnlock()
	result := make(map[string]string, len(inspector.providerModes))
	for provider, mode := range inspector.providerModes {
		result[provider] = string(mode)
	}
	return result
}

// ModeFor answers the mode one request is actually inspected under. The
// global off wins; otherwise a provider override replaces the default.
func (inspector *Inspector) ModeFor(providerID string) domain.Mode {
	inspector.mu.RLock()
	defer inspector.mu.RUnlock()
	if inspector.mode == domain.ModeOff {
		return domain.ModeOff
	}
	if mode, ok := inspector.providerModes[providerID]; ok {
		return mode
	}
	return inspector.mode
}

// RuleCount reports how many rules are loaded, for the operator's confidence.
func (inspector *Inspector) RuleCount() int { return inspector.engine.RuleCount() }

// IndicatorCount reports how many literal indicators are loaded.
func (inspector *Inspector) IndicatorCount() int { return inspector.engine.IndicatorCount() }

// RuleSetVersion reports the vendored rule set version.
func (inspector *Inspector) RuleSetVersion() int { return inspector.engine.Version() }

// Inspect judges one finished answer. In ModeOff it does no work at all — not
// even extraction — so a user who turns the guardrails off pays nothing. The
// mode is this provider's: an override that hardens one distrusted reseller
// must not change what any other provider's answers are judged under.
func (inspector *Inspector) Inspect(body []byte, eventStream bool, subject Subject) Decision {
	mode := inspector.ModeFor(subject.ProviderID)
	if !mode.Inspects() || len(body) == 0 {
		return Decision{Verdict: domain.VerdictClean}
	}
	extraction := Extract(body, eventStream)
	findings := make([]domain.Finding, 0, 8)
	// The same payload reaches the engine more than once by design — a tool call is
	// scanned as raw JSON and again decoded — so identical evidence is reported once.
	// Deduplication is on rule and match, not on the piece: two different payloads
	// that trip the same rule are two findings.
	//
	// The KIND of place is part of the key, because where the provider put a payload
	// is the finding. An assistant explaining `curl x | sh` in prose is something
	// honest models do all day; the same string in the arguments of a tool call is the
	// client about to run it. Piece order follows the body, so the provider chooses it:
	// on a key without the place, the attacker would decide which of the two
	// attributions survives, and would put the prose first.
	//
	// The kind is coarse — every tool call counts as one place, not one per name — and
	// that is the point: keying on the exact name would let a provider restate one
	// payload across forty differently-named calls and push a genuinely different
	// detection out of MaxFindings. The operator needs "it was in an executed call",
	// not the forty names.
	seen := make(map[string]struct{}, 8)
	for _, piece := range extraction.Pieces {
		for _, finding := range inspector.engine.Inspect(piece.Text, piece.Source) {
			key := finding.RuleID + "\x00" + finding.Match + "\x00" + sourceKind(finding.Source)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			findings = append(findings, finding)
		}
	}
	// A tool call the client cannot have asked for is an anomaly no pattern can
	// see: it needs both halves of the exchange.
	if !subject.ClientDeclaredTools {
		for _, name := range extraction.ToolNames {
			findings = append(findings, domain.UnsolicitedToolFinding(name))
		}
	}
	// An answer that outgrew the budget was partly forwarded unread. Saying so is the
	// difference between "nothing was found" and "nothing was looked at", and padding
	// past the ceiling is the cheapest evasion there is.
	if extraction.Truncated {
		findings = append(findings, domain.TruncatedInspectionFinding())
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
	providerID := truncate(subject.ProviderID, 64)
	model := truncate(subject.Model, 128)
	inspector.mu.Lock()
	// A record whose only finding is "part of this answer was never read" is
	// bookkeeping, not a detection, and one provider can produce it on every single
	// answer just by being wordy. Left to accumulate it would push every real finding
	// out of a bounded store — the cheapest evasion there is: pad each answer past the
	// budget until the evidence of the one that mattered has fallen off the end. So one
	// such record is kept per provider, carrying how often it happened and when it last
	// did, and it competes for exactly one slot instead of all of them.
	if bookkeepingOnly(decision.Findings) {
		for index := range inspector.records {
			existing := inspector.records[index]
			if existing.ProviderID != providerID || !bookkeepingOnly(existing.Findings) {
				continue
			}
			existing.At = inspector.now().UTC()
			existing.Occurrences++
			if existing.Model != model {
				// It stopped being about one model the moment it stood for several.
				existing.Model = ""
			}
			inspector.records = append(inspector.records[:index], inspector.records[index+1:]...)
			inspector.records = append([]Record{existing}, inspector.records...)
			inspector.mu.Unlock()
			inspector.notify(existing)
			return
		}
	}
	inspector.sequence++
	record := Record{
		ID:           "gr_" + formatUint(inspector.sequence),
		At:           inspector.now().UTC(),
		Verdict:      decision.Verdict,
		Severity:     decision.Severity.String(),
		ProviderID:   providerID,
		ProviderName: truncate(subject.ProviderName, 80),
		Model:        model,
		Findings:     redactEvidence(decision.Findings, subject.Secrets),
		Occurrences:  1,
	}
	inspector.records = append([]Record{record}, inspector.records...)
	if len(inspector.records) > inspector.capacity {
		inspector.records = inspector.records[:inspector.capacity]
	}
	inspector.mu.Unlock()

	inspector.notify(record)
}

// bookkeepingOnly reports whether every finding is about the inspection itself
// rather than about what the provider sent.
func bookkeepingOnly(findings []domain.Finding) bool {
	if len(findings) == 0 {
		return false
	}
	for _, finding := range findings {
		if finding.RuleID != domain.RuleInspectionTruncated {
			return false
		}
	}
	return true
}

// minEvidenceSecretBytes is the shortest credential fragment worth scrubbing out of
// recorded evidence. Shorter fragments also match ordinary prose, so editing those
// would mangle the evidence; the relay's own filed-text redaction uses the same
// width.
const minEvidenceSecretBytes = 8

// redactEvidence scrubs the markers of the attempt in flight out of every
// field of a recorded finding that carries answer bytes: the match, the
// excerpt, and the source. The source is built from the answer's tool names,
// which the provider picks — an echoed key can travel as a name just as
// easily as inside an argument. The verdict is unaffected: the decision was
// made on the real bytes, and only what the journal keeps — and the findings
// page then shows — is cleaned.
func redactEvidence(findings []domain.Finding, secrets []string) []domain.Finding {
	if len(findings) == 0 || len(secrets) == 0 {
		return findings
	}
	redacted := make([]domain.Finding, len(findings))
	for index, finding := range findings {
		finding.Match = redactSecretText(finding.Match, secrets)
		finding.Excerpt = redactSecretText(finding.Excerpt, secrets)
		finding.Source = redactSecretText(finding.Source, secrets)
		redacted[index] = finding
	}
	return redacted
}

func redactSecretText(value string, secrets []string) string {
	for _, secret := range secrets {
		if len(secret) >= minEvidenceSecretBytes && strings.Contains(value, secret) {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func (inspector *Inspector) notify(record Record) {
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
