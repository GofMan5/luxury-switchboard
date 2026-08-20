package domain_test

import (
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
)

func TestModeParsingRejectsAnythingElse(t *testing.T) {
	for _, value := range []string{"off", "monitor", "block"} {
		if _, err := domain.ParseMode(value); err != nil {
			t.Fatalf("%q must be a valid mode: %v", value, err)
		}
	}
	// A typo must fail loudly instead of quietly disabling the guardrails.
	for _, value := range []string{"", "Off", "blocking", "monitor ", "on"} {
		if _, err := domain.ParseMode(value); err == nil {
			t.Fatalf("%q must not be accepted as a mode", value)
		}
	}
}

func TestOnlyMonitorAndBlockDoWork(t *testing.T) {
	if domain.ModeOff.Inspects() {
		t.Fatal("off must not inspect")
	}
	if !domain.ModeMonitor.Inspects() || !domain.ModeBlock.Inspects() {
		t.Fatal("monitor and block must inspect")
	}
}

func TestVerdictsFollowModeAndSeverity(t *testing.T) {
	high := []domain.Finding{{RuleID: "dl-curl-pipe-sh", Severity: "high"}}
	medium := []domain.Finding{{RuleID: "dl-webclient-download", Severity: "medium"}}

	cases := []struct {
		name     string
		mode     domain.Mode
		findings []domain.Finding
		want     domain.Verdict
	}{
		{"nothing found is clean in monitor", domain.ModeMonitor, nil, domain.VerdictClean},
		{"nothing found is clean in block", domain.ModeBlock, nil, domain.VerdictClean},
		{"monitor never blocks", domain.ModeMonitor, high, domain.VerdictAlert},
		{"block refuses high", domain.ModeBlock, high, domain.VerdictBlocked},
		// Blocking on medium would destroy legitimate answers: these rules match on
		// shell and network idiom an honest assistant produces constantly.
		{"block forwards medium", domain.ModeBlock, medium, domain.VerdictAlert},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.mode.Decide(testCase.findings); got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}
