// Package domain implements the guardrails detection engine: a stateless,
// immutable matcher that scans assistant text and tool-call arguments taken from
// a provider's answer against behavioural regular expressions and a literal
// indicator blocklist.
//
// The threat is a hostile or compromised provider answering with content that
// steers the caller's AI client into running something harmful — a payload piped
// into a shell, a persistence entry, a credential read, an exfiltration hop —
// including tool calls the client never asked for. Switchboard sits between the
// client and every provider, so it is the only place that can see this for all
// of them at once.
//
// The engine is deliberately free of transport, storage and configuration so it
// can run inline on the response path and be tested on plain strings.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Severity ranks how dangerous a finding is. Only High justifies refusing a
// response; the lower ranks are for the operator to read.
type Severity int

const (
	SeverityLow Severity = iota
	SeverityMedium
	SeverityHigh
)

// Bounds keep a hostile answer from turning a finding into a memory or log
// problem. A match is evidence, not a copy of the response.
const (
	maxMatchBytes   = 160
	maxExcerptBytes = 200
	excerptPadding  = 48

	// MaxFindings caps how many findings one inspection reports. The verdict only
	// needs the highest severity, so a body engineered to trip every rule cannot
	// grow the record without bound.
	MaxFindings = 32
)

func (severity Severity) String() string {
	switch severity {
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	default:
		return "low"
	}
}

// ParseSeverity maps a rule's textual severity onto the enum. Anything
// unrecognised reads as Low so a malformed rule cannot silently start blocking
// traffic.
func ParseSeverity(value string) Severity {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return SeverityHigh
	case "medium":
		return SeverityMedium
	default:
		return SeverityLow
	}
}

// RuleUnsolicitedTool is the synthetic rule reported when a provider answers
// with a tool call although the client declared no tools at all. No regular
// expression can see that; it needs both sides of the exchange.
const RuleUnsolicitedTool = "proto-tooluse-unsolicited"

// CategoryProtocol groups the synthetic findings that come from comparing the
// request with the answer rather than from matching text.
const CategoryProtocol = "protocol-anomaly"

type ruleSpec struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Severity    string `json:"severity"`
	Pattern     string `json:"pattern"`
	Description string `json:"description"`
}

type rulesDocument struct {
	Version int        `json:"version"`
	Rules   []ruleSpec `json:"rules"`
}

type blocklistDocument struct {
	Domains      []string `json:"domains"`
	IPs          []string `json:"ips"`
	Paths        []string `json:"paths"`
	TaskNames    []string `json:"task_names"`
	ProcessNames []string `json:"process_names"`
	Hashes       []string `json:"hashes"`
}

type compiledRule struct {
	spec     ruleSpec
	severity Severity
	pattern  *regexp.Regexp
}

type indicator struct {
	term  string
	lower string
	kind  string
}

// Finding is one detection hit. Every field is either fixed rule metadata or a
// bounded excerpt: a finding is safe to keep in memory and show to the operator,
// and it never carries the prompt, the full answer or a credential.
type Finding struct {
	RuleID      string `json:"ruleId"`
	Category    string `json:"category"`
	Severity    string `json:"severity"`
	Match       string `json:"match"`
	Excerpt     string `json:"excerpt"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`
}

// Engine is a compiled, immutable, concurrency-safe detector. Build it once and
// share it across requests.
type Engine struct {
	rules      []compiledRule
	indicators []indicator
	version    int
}

// NewEngine compiles an engine from raw rules and blocklist JSON. A rule that
// does not compile is an error rather than a skipped rule: silently running with
// fewer rules than the operator believes is worse than failing the build.
func NewEngine(rulesJSON, blocklistJSON []byte) (*Engine, error) {
	var document rulesDocument
	if err := json.Unmarshal(rulesJSON, &document); err != nil {
		return nil, fmt.Errorf("parse guardrail rules: %w", err)
	}
	if len(document.Rules) == 0 {
		return nil, errors.New("guardrail rules are empty")
	}
	engine := &Engine{version: document.Version, rules: make([]compiledRule, 0, len(document.Rules))}
	seen := make(map[string]struct{}, len(document.Rules))
	for _, spec := range document.Rules {
		if spec.ID == "" || spec.Pattern == "" {
			return nil, errors.New("guardrail rule is missing an id or a pattern")
		}
		if _, duplicate := seen[spec.ID]; duplicate {
			return nil, fmt.Errorf("duplicate guardrail rule %q", spec.ID)
		}
		seen[spec.ID] = struct{}{}
		pattern, err := regexp.Compile(spec.Pattern)
		if err != nil {
			return nil, fmt.Errorf("guardrail rule %q has an invalid pattern: %w", spec.ID, err)
		}
		engine.rules = append(engine.rules, compiledRule{spec: spec, severity: ParseSeverity(spec.Severity), pattern: pattern})
	}
	if len(blocklistJSON) > 0 {
		var blocklist blocklistDocument
		if err := json.Unmarshal(blocklistJSON, &blocklist); err != nil {
			return nil, fmt.Errorf("parse guardrail blocklist: %w", err)
		}
		groups := []struct {
			kind  string
			terms []string
		}{
			{"domain", blocklist.Domains}, {"ip", blocklist.IPs}, {"path", blocklist.Paths},
			{"task", blocklist.TaskNames}, {"process", blocklist.ProcessNames}, {"hash", blocklist.Hashes},
		}
		for _, group := range groups {
			for _, term := range group.terms {
				term = strings.TrimSpace(term)
				if term == "" {
					continue
				}
				engine.indicators = append(engine.indicators, indicator{term: term, lower: strings.ToLower(term), kind: group.kind})
			}
		}
	}
	return engine, nil
}

// RuleCount reports how many behavioural rules are loaded. The operator sees
// this number; the rules themselves stay inside the binary.
func (engine *Engine) RuleCount() int { return len(engine.rules) }

// IndicatorCount reports how many literal indicators are loaded.
func (engine *Engine) IndicatorCount() int { return len(engine.indicators) }

// Version reports the vendored rule set version.
func (engine *Engine) Version() int { return engine.version }

// Inspect scans one piece of text. source is a short safe label describing where
// the text came from, such as "assistant_text" or "tool_call:sh_cmd".
//
// Every rule is evaluated before the cap is applied. Stopping early would make
// MaxFindings a first-come limit, and since the rule set is public a provider
// could put enough low-severity idiom in front of its payload to stop the scan
// before the rule that matters — which in block mode is the whole decision. The
// work is bounded by the rule count either way: each rule reports its first match
// only, so this is at most one finding per rule.
func (engine *Engine) Inspect(text, source string) []Finding {
	if text == "" {
		return nil
	}
	findings := make([]Finding, 0, 8)
	seen := make(map[string]struct{}, 8)
	add := func(finding Finding) {
		key := finding.RuleID + "\x00" + finding.Match
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		findings = append(findings, finding)
	}
	for _, rule := range engine.rules {
		location := rule.pattern.FindStringIndex(text)
		if location == nil {
			continue
		}
		add(Finding{
			RuleID: rule.spec.ID, Category: rule.spec.Category, Severity: rule.severity.String(),
			Match:   truncate(text[location[0]:location[1]], maxMatchBytes),
			Excerpt: excerpt(text, location[0], location[1]), Source: source,
			Description: rule.spec.Description,
		})
	}
	if len(engine.indicators) > 0 {
		lowered := strings.ToLower(text)
		for _, term := range engine.indicators {
			index := strings.Index(lowered, term.lower)
			if index < 0 {
				continue
			}
			add(Finding{
				RuleID: "ioc-" + term.kind, Category: "ioc", Severity: SeverityHigh.String(),
				Match:   truncate(term.term, maxMatchBytes),
				Excerpt: excerpt(text, index, index+len(term.lower)), Source: source,
				Description: "Known indicator of compromise (" + term.kind + ")",
			})
		}
	}
	SortFindings(findings)
	if len(findings) > MaxFindings {
		findings = findings[:MaxFindings]
	}
	return findings
}

// UnsolicitedToolFinding reports a tool call in an answer to a request that
// declared no tools. toolName is the provider-reported name, which is metadata
// rather than content, so it is safe to keep.
func UnsolicitedToolFinding(toolName string) Finding {
	return Finding{
		RuleID: RuleUnsolicitedTool, Category: CategoryProtocol, Severity: SeverityHigh.String(),
		Match: truncate(toolName, maxMatchBytes), Source: "tool_call:" + truncate(toolName, 64),
		Description: "The provider answered with a tool call although the client declared no tools",
	}
}

// MaxSeverity returns the highest severity among findings.
func MaxSeverity(findings []Finding) Severity {
	highest := SeverityLow
	for _, finding := range findings {
		if severity := ParseSeverity(finding.Severity); severity > highest {
			highest = severity
		}
	}
	return highest
}

// SortFindings orders findings by descending severity then rule id, so logs and
// tests see a deterministic order.
func SortFindings(findings []Finding) {
	sort.SliceStable(findings, func(left, right int) bool {
		leftSeverity, rightSeverity := ParseSeverity(findings[left].Severity), ParseSeverity(findings[right].Severity)
		if leftSeverity != rightSeverity {
			return leftSeverity > rightSeverity
		}
		return findings[left].RuleID < findings[right].RuleID
	})
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	// Cutting mid-rune would put replacement characters into the record, so the
	// cut moves back to a rune boundary.
	cut := limit
	for cut > 0 && !isRuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func isRuneStart(character byte) bool {
	return character&0xC0 != 0x80
}

// excerpt returns a whitespace-collapsed, length-bounded window around the match
// so the operator sees enough context to judge it without the record growing
// into a copy of the answer.
func excerpt(text string, start, end int) string {
	low := max(start-excerptPadding, 0)
	high := min(end+excerptPadding, len(text))
	return truncate(strings.Join(strings.Fields(text[low:high]), " "), maxExcerptBytes)
}
