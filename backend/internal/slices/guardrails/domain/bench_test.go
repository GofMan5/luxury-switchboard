package domain_test

import (
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
)

// Inspection runs inline on the response path, so its cost is the caller's wait.
// It exists as a benchmark because the number was once bad enough to matter: 106
// case-folded expressions each scanning the whole answer took 6.6 ms for one
// kilobyte and 4.2 s at the half-megabyte ceiling. Skipping a rule whose required
// literal is absent brought that to 0.11 ms and 59 ms on the same machine.
//
// A regression here is a provider answer arriving seconds late for no reason. This
// has no threshold and CI does not run benchmarks, so the property is asserted as a
// rule count in TestOrdinaryProseReachesAlmostNoRule; what this measures is the
// wall-clock that count stands for.
func BenchmarkInspectOrdinaryProse(b *testing.B) {
	built, err := domain.NewEngine(ruleset.RulesJSON, ruleset.BlocklistJSON)
	if err != nil {
		b.Fatalf("engine: %v", err)
	}
	prose := strings.Repeat("The relay buffers the answer and then forwards it to the client, "+
		"which is what the retry ladder protects. Run go test ./... to check. ", 4_000)
	for name, size := range map[string]int{"1KiB": 1024, "128KiB": 128 * 1024, "512KiB": 512 * 1024} {
		text := prose[:size]
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			for b.Loop() {
				if findings := built.Inspect(text, "assistant_text"); len(findings) != 0 {
					b.Fatalf("the benchmark corpus is supposed to be clean: %+v", findings)
				}
			}
		})
	}
}
