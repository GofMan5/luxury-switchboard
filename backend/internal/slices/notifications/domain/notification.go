package domain

import "time"

// Severity carries how a notification is presented: the tone of its toast and
// its badge. It is presentation metadata, not a priority queue — every
// notification is delivered, and bounded history keeps them all the same size.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeveritySuccess Severity = "success"
	SeverityWarning Severity = "warning"
	SeverityDanger  Severity = "danger"
)

func (severity Severity) Valid() bool {
	switch severity {
	case SeverityInfo, SeveritySuccess, SeverityWarning, SeverityDanger:
		return true
	default:
		return false
	}
}

// Kind is the stable identifier of a notification's source: a failover is
// "provider_failover", a dead key is "key_dead". The feed deduplicates and the
// settings gate by it, so a kind is a contract, not a display string.
type Kind string

const (
	KindProviderFailover Kind = "provider_failover"
	KindProviderHealth   Kind = "provider_health"
	KindKeyHealth        Kind = "key_dead"
	KindBalanceExhausted Kind = "balance_exhausted"
	KindHistoryDrop      Kind = "history_drop"
	KindCodexAuth        Kind = "codex_auth"
)

// Notification is one thing the operator should know happened. Titles and
// bodies are pre-written by the source slice and must already be free of
// provider metadata the public boundary forbids: these travel over stdio in
// both editions.
type Notification struct {
	ID       string    `json:"id"`
	Kind     Kind      `json:"kind"`
	Severity Severity  `json:"severity"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	At       time.Time `json:"at"`
}

// DeadKeyThreshold is how many authentication refusals in a row read as a dead
// credential. Two might be a flaky provider; three is a verdict.
const DeadKeyThreshold = 3
