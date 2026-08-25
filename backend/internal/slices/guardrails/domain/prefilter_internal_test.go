package domain

import (
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
)

// The cost of inspection is how many of the 106 expressions actually run, and on
// ordinary prose that number should be near zero. BenchmarkInspectOrdinaryProse
// measures the wall-clock, but a benchmark has no threshold and CI never runs it, so
// the regression it exists to catch — an answer arriving seconds late for no reason —
// would ship. This test asserts the same property as a count, which is deterministic
// and does not care what machine it runs on.
//
// It lives inside the package because the alternative is exporting the rule list, and
// an export invented for a test is worse than a test in the package.
func TestOrdinaryProseReachesAlmostNoRule(t *testing.T) {
	built, err := NewEngine(ruleset.RulesJSON, ruleset.BlocklistJSON)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	prose := strings.Repeat("The relay buffers the answer and then forwards it to the client, "+
		"which is what the retry ladder protects. Run go test ./... to check. ", 64)
	lowered := strings.ToLower(prose)

	var reached []string
	for _, rule := range built.rules {
		if mayContain(lowered, rule.literals) {
			reached = append(reached, rule.spec.ID)
		}
	}
	// Zero is the honest expectation: this text is prose. The allowance exists so that a
	// newly vendored rule with a genuinely common literal is a one-line change here
	// rather than a mystery, and it is small enough that losing the prefilter fails.
	if len(reached) > 3 {
		t.Fatalf("%d of %d rules run on ordinary prose, so inspection is paying for most of the set: %v",
			len(reached), len(built.rules), reached)
	}

	// A rule that proves no literal at all runs on every answer forever, which is the
	// one thing the prefilter cannot make cheaper. `obf-caret-backtick` was that rule
	// until its two single-character literals stopped being discarded, and it cost the
	// entire 81 ms measured at the half-megabyte ceiling.
	var unfiltered []string
	for _, rule := range built.rules {
		if len(rule.literals) == 0 {
			unfiltered = append(unfiltered, rule.spec.ID)
		}
	}
	if len(unfiltered) != 0 {
		t.Fatalf("these rules prove no required literal, so they scan every answer in full: %v", unfiltered)
	}
}

// The prefilter searches a lowercased copy of the answer for a lowercased literal, and
// that only works while RE2's idea of case-folding agrees with strings.ToLower. It
// disagrees about exactly one rune in Unicode: ſ (U+017F) folds onto `s` under (?i) and
// ToLower leaves it as it is. So `~/.awſ/credentials` matches cred-aws-read, while a
// prefilter searching for `~/.aws/credentials` skips the rule and reports nothing —
// which is the one thing the prefilter is not allowed to do.
//
// This is a property of every rule with an `s` in its literals, not of that one rule,
// so the whole set is checked rather than a hand-picked example.
func TestTheOneRuneWhereCaseFoldingDisagreesCannotHideAPayload(t *testing.T) {
	built, err := NewEngine(ruleset.RulesJSON, ruleset.BlocklistJSON)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	var checked int
	for _, rule := range built.rules {
		for _, literal := range rule.literals {
			if !strings.Contains(literal, "s") {
				continue
			}
			// The literal is a string the pattern needs, so a text built from it either
			// matches or proves nothing; only a match tells us anything here.
			plain := literal
			if rule.pattern.FindStringIndex(plain) == nil {
				continue
			}
			swapped := strings.Replace(plain, "s", "ſ", 1)
			if rule.pattern.FindStringIndex(swapped) == nil {
				continue
			}
			checked++
			if len(built.Inspect(swapped, "assistant_text")) == 0 {
				t.Errorf("rule %s matches %q, but the prefilter skipped it: a long s hides the payload", rule.spec.ID, swapped)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no rule was exercised, so this test is checking nothing")
	}
}
