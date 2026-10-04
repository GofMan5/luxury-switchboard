package domain_test

import (
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
)

// The two rules this project itself changed carry their own pins, because the
// corpus does not: upstream's samples trip the branches upstream already
// matched, and the branch a fix added is exactly the one nobody had sampled.
func builtEngine(t *testing.T) *domain.Engine {
	t.Helper()
	built, err := domain.NewEngine(ruleset.RulesJSON, ruleset.BlocklistJSON)
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// v7's headline fix: `certutil -decode payload.b64 payload.exe`, the ordinary
// way to stage a payload, went undetected while `certutil x-decode` matched.
// The corpus's -urlcache sample cannot regress this; a direct pin can.
func TestCertutilDecodeIsDetected(t *testing.T) {
	built := builtEngine(t)
	findings := built.Inspect("certutil -decode payload.b64 payload.exe", "corpus")
	found := false
	for _, finding := range findings {
		if finding.RuleID == "dl-certutil" {
			found = true
		}
	}
	if !found {
		t.Fatal("certutil -decode staged a payload undetected: the v7 fix regressed")
	}
}

// The rule this tree added because nothing in the set caught it: a decoded
// payload executed directly. Upstream had no sample for a rule that did not
// exist there.
func TestDecodedPipeExecIsDetected(t *testing.T) {
	built := builtEngine(t)
	for _, payload := range []string{
		"echo aGVsbG8= | base64 -d | sh",
		"[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(\"...\")) | iex",
	} {
		findings := built.Inspect(payload, "corpus")
		found := false
		for _, finding := range findings {
			if finding.RuleID == "obf-decode-pipe-exec" {
				found = true
			}
		}
		if !found {
			t.Fatalf("a decoded payload executed directly went undetected: %q", payload)
		}
	}
}
