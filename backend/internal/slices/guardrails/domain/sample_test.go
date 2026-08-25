package domain_test

import (
	"encoding/json"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
)

// The literal prefilter is only allowed to cost time, never a finding, and the way to
// prove that is to hand every rule a string its own expression matches and require the
// engine to report it. Random prose cannot do this job: measured against the rule set,
// a fuzz corpus of the set's own vocabulary reaches sixteen rules and leaves ninety
// never matched once, so a literal derived wrongly for any of those ninety silently
// disables the rule and the suite stays green.
//
// The samples are built from each pattern's own syntax tree, one per branch of every
// alternation, because a literal has to hold for every branch and not just the first.
// This found `dl-certutil`, where seven of eight switches could never match.
func sampleOf(builder *strings.Builder, node *syntax.Regexp, choices map[*syntax.Regexp]int) {
	switch node.Op {
	case syntax.OpLiteral:
		for _, letter := range node.Rune {
			builder.WriteRune(letter)
		}
	case syntax.OpCharClass:
		// A space is preferred over a letter: a class standing for "anything between two
		// tokens" is nearly always a gap, and closing it glues the tokens into one word,
		// losing the boundary the rule asks for on either side.
		for index := 0; index+1 < len(node.Rune); index += 2 {
			if node.Rune[index] <= ' ' && ' ' <= node.Rune[index+1] {
				builder.WriteByte(' ')
				return
			}
		}
		for index := 0; index+1 < len(node.Rune); index += 2 {
			low, high := node.Rune[index], node.Rune[index+1]
			for candidate := low; candidate <= high && candidate < low+96; candidate++ {
				if candidate >= 'a' && candidate <= 'z' {
					builder.WriteRune(candidate)
					return
				}
			}
		}
		if len(node.Rune) > 0 {
			builder.WriteRune(node.Rune[0])
		}
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		builder.WriteByte('x')
	case syntax.OpCapture:
		sampleOf(builder, node.Sub[0], choices)
	case syntax.OpConcat:
		for _, sub := range node.Sub {
			sampleOf(builder, sub, choices)
		}
	case syntax.OpAlternate:
		sampleOf(builder, node.Sub[choices[node]%len(node.Sub)], choices)
	case syntax.OpPlus, syntax.OpStar, syntax.OpQuest:
		// One repetition even where zero is legal, for the same reason: an optional gap
		// that collapses joins its neighbours and drops the boundary.
		sampleOf(builder, node.Sub[0], choices)
	case syntax.OpRepeat:
		for range max(node.Min, 1) {
			sampleOf(builder, node.Sub[0], choices)
		}
	}
}

// alternates collects every choice point so each branch can be sampled in turn.
func alternates(node *syntax.Regexp, found *[]*syntax.Regexp) {
	if node.Op == syntax.OpAlternate {
		*found = append(*found, node)
	}
	for _, sub := range node.Sub {
		alternates(sub, found)
	}
}

func samplesOf(pattern string) []string {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	simplified := parsed.Simplify()
	var choicePoints []*syntax.Regexp
	alternates(simplified, &choicePoints)
	samples := make([]string, 0, 8)
	emit := func(choices map[*syntax.Regexp]int) {
		var builder strings.Builder
		sampleOf(&builder, simplified, choices)
		samples = append(samples, builder.String())
	}
	emit(nil)
	// One variant per branch rather than every combination: the claim is that no branch
	// is unreachable, and the product of a dozen choice points is not a test.
	for _, point := range choicePoints {
		for branch := 1; branch < len(point.Sub); branch++ {
			emit(map[*syntax.Regexp]int{point: branch})
		}
	}
	return samples
}

func TestEveryRuleReportsAStringItsOwnPatternMatches(t *testing.T) {
	built := engine(t)
	var document struct {
		Rules []struct {
			ID      string `json:"id"`
			Pattern string `json:"pattern"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(ruleset.RulesJSON, &document); err != nil {
		t.Fatalf("rules: %v", err)
	}

	var checked int
	for _, rule := range document.Rules {
		pattern := regexp.MustCompile(rule.Pattern)
		samples := samplesOf(rule.Pattern)
		if len(samples) == 0 {
			t.Fatalf("rule %s produced no sample, so it is not covered here", rule.ID)
		}
		for _, sample := range samples {
			// A sample the rule's own expression rejects means this generator is wrong
			// about the pattern, or the pattern cannot match what it claims to. Either
			// way it must be looked at rather than skipped: skipping is how ninety
			// unmatched rules went unnoticed in the first place.
			if !pattern.MatchString(sample) {
				t.Errorf("rule %s cannot match a string built from its own pattern: %q", rule.ID, sample)
				continue
			}
			checked++
			var reported bool
			for _, finding := range built.Inspect(sample, "assistant_text") {
				reported = reported || finding.RuleID == rule.ID
			}
			if !reported {
				t.Errorf("rule %s was skipped by the literal prefilter on %q, which its own pattern matches", rule.ID, sample)
			}
		}
	}
	// Guards the generator itself: a change that made samplesOf return one trivial
	// string per rule would otherwise leave this test passing and covering nothing.
	if checked < 4*len(document.Rules) {
		t.Fatalf("only %d samples across %d rules — the branches of the patterns are no longer being covered", checked, len(document.Rules))
	}
	if built.RuleCount() != len(document.Rules) {
		t.Fatalf("the engine loaded %d of %d rules", built.RuleCount(), len(document.Rules))
	}
}
