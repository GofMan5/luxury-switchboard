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
	"regexp/syntax"
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

// RuleInspectionTruncated is the synthetic rule reported when an answer was larger
// than the inspection budget, so part of it reached the client unread. It is low
// severity — nothing was found, and nothing can be claimed — but it is reported,
// because padding an answer past the ceiling is the cheapest way to buy silence and
// the operator must not read a clean record as full coverage.
const RuleInspectionTruncated = "proto-inspection-truncated"

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
	// literals are lowercased strings of which at least one must appear in any text
	// this rule can match. Empty means the pattern proved nothing, so it always runs.
	literals []string
}

type indicator struct {
	term     string
	lower    string
	kind     string
	severity Severity
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
		engine.rules = append(engine.rules, compiledRule{
			spec: spec, severity: ParseSeverity(spec.Severity), pattern: pattern,
			literals: requiredLiterals(spec.Pattern),
		})
	}
	if len(blocklistJSON) > 0 {
		var blocklist blocklistDocument
		if err := json.Unmarshal(blocklistJSON, &blocklist); err != nil {
			return nil, fmt.Errorf("parse guardrail blocklist: %w", err)
		}
		groups := []struct {
			kind     string
			severity Severity
			terms    []string
		}{
			{"domain", SeverityHigh, blocklist.Domains},
			{"ip", SeverityHigh, blocklist.IPs},
			{"path", SeverityHigh, blocklist.Paths},
			{"hash", SeverityHigh, blocklist.Hashes},
			// A task or process name is a bare word, not a locator. The list holds
			// `proxy.exe`, `CodeAssist` and `StartupOptimizer`: an honest answer about a
			// local proxy or an IDE assistant contains them, and at high severity Block
			// mode would refuse it. The campaign that actually creates that task also
			// carries the schtasks command or the full path, and those still match high,
			// so nothing is lost but the false refusal.
			{"task", SeverityMedium, blocklist.TaskNames},
			{"process", SeverityMedium, blocklist.ProcessNames},
		}
		for _, group := range groups {
			for _, term := range group.terms {
				term = strings.TrimSpace(term)
				if term == "" {
					continue
				}
				engine.indicators = append(engine.indicators, indicator{
					term: term, lower: strings.ToLower(term), kind: group.kind, severity: group.severity,
				})
			}
		}
	}
	return engine, nil
}

// requiredLiterals returns lowercased strings of which at least one must appear in
// any text the pattern can match, or nil when the pattern proves no such string.
//
// It is a filter, never a decision: a rule keeps its own regular expression as the
// only thing that reports a finding. The point is that 106 case-folded expressions
// over half a megabyte cost seconds of CPU inline on the response path, while
// substring search over the same bytes costs milliseconds — and a rule whose
// literal is absent cannot match, so it does not have to run at all.
//
// Everything unproven yields nil, which means the rule always runs. Being wrong in
// that direction costs time; being wrong the other way would silently disable a
// rule.
func requiredLiterals(pattern string) []string {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	literals := literalsOf(parsed.Simplify())
	if len(literals) == 0 {
		return nil
	}
	// A single character was excluded here on the theory that it filters nothing an
	// ordinary answer does not already contain. Measured, that theory was wrong in the
	// one case it applied to: `obf-caret-backtick` requires a caret or a backtick, and
	// prose contains neither, so discarding those two characters left the only rule that
	// ran on clean text — and it accounted for the entire inspection cost, 81 ms of the
	// 81 ms at the half-megabyte ceiling. A literal is kept whatever its length; a
	// character common enough to always hit costs one substring scan to learn that.
	return literals
}

// literalsOf walks the parsed pattern. A concatenation may pick any one of its
// parts, an alternation needs every branch to promise something, and anything
// optional or open-ended promises nothing.
func literalsOf(expression *syntax.Regexp) []string {
	switch expression.Op {
	case syntax.OpLiteral:
		text := strings.ToLower(string(expression.Rune))
		for _, character := range text {
			// Case folding outside ASCII is not a straight lowercase, so those literals
			// are not trusted as a filter.
			if character > 127 {
				return nil
			}
		}
		if text == "" {
			return nil
		}
		return []string{text}
	case syntax.OpCapture:
		return literalsOf(expression.Sub[0])
	case syntax.OpPlus:
		return literalsOf(expression.Sub[0])
	case syntax.OpRepeat:
		if expression.Min < 1 {
			return nil
		}
		return literalsOf(expression.Sub[0])
	case syntax.OpConcat:
		// Any single part is a valid requirement for the whole, so the most selective
		// one wins.
		var best []string
		for _, part := range expression.Sub {
			candidate := literalsOf(part)
			if len(candidate) == 0 {
				continue
			}
			if len(best) == 0 || shortestLength(candidate) > shortestLength(best) {
				best = candidate
			}
		}
		return best
	case syntax.OpAlternate:
		// One branch that promises nothing makes the whole alternation promise nothing.
		union := make([]string, 0, len(expression.Sub))
		for _, branch := range expression.Sub {
			candidate := literalsOf(branch)
			if len(candidate) == 0 {
				return nil
			}
			union = append(union, candidate...)
		}
		return union
	default:
		return nil
	}
}

func shortestLength(literals []string) int {
	shortest := len(literals[0])
	for _, literal := range literals[1:] {
		shortest = min(shortest, len(literal))
	}
	return shortest
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
	lowered := strings.ToLower(text)
	// RE2 folds ſ (U+017F) onto `s` under (?i) and strings.ToLower leaves it alone. It is
	// the ONLY rune in Unicode where the two disagree about an ASCII letter — enumerated,
	// not assumed — and the disagreement is a real bypass: `~/.awſ/credentials` matches
	// cred-aws-read while the prefilter, searching for the literal `~/.aws/credentials`,
	// skips the rule and reports nothing. That is the one thing the prefilter is not
	// allowed to do.
	//
	// Only the prefilter reads this string. Every match position comes from the pattern
	// run against the original text, so collapsing a two-byte rune to one byte here
	// cannot move an excerpt. When the rune is absent — always, in practice — ReplaceAll
	// returns the same string without allocating.
	filterable := strings.ReplaceAll(lowered, "ſ", "s")
	for _, rule := range engine.rules {
		if !mayContain(filterable, rule.literals) {
			continue
		}
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
	for _, term := range engine.indicators {
		index := strings.Index(lowered, term.lower)
		if index < 0 {
			continue
		}
		add(Finding{
			RuleID: "ioc-" + term.kind, Category: "ioc", Severity: term.severity.String(),
			Match:   truncate(term.term, maxMatchBytes),
			Excerpt: excerpt(text, index, index+len(term.lower)), Source: source,
			Description: "Known indicator of compromise (" + term.kind + ")",
		})
	}
	SortFindings(findings)
	if len(findings) > MaxFindings {
		findings = findings[:MaxFindings]
	}
	return findings
}

// mayContain reports whether text can possibly match a rule with these literals. A
// rule that promised nothing always runs.
func mayContain(lowered string, literals []string) bool {
	if len(literals) == 0 {
		return true
	}
	for _, literal := range literals {
		if strings.Contains(lowered, literal) {
			return true
		}
	}
	return false
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

// TruncatedInspectionFinding reports that the answer outgrew the inspection budget.
func TruncatedInspectionFinding() Finding {
	return Finding{
		RuleID: RuleInspectionTruncated, Category: CategoryProtocol, Severity: SeverityLow.String(),
		Source:      "inspection",
		Description: "The answer was larger than the inspection budget, so part of it was forwarded unread",
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
