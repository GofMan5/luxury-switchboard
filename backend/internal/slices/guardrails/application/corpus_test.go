package application_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
)

// The corpus is vendored from holone's own test set (MIT,
// internal/inspect/testdata/corpus): every malicious sample must trip at least
// one rule, every clean sample must trip none. Our vendored rule set narrows
// eight of upstream's patterns so honest developer answers stop being refused;
// this corpus is the regression that proves the narrowing never disarmed the
// attacks those patterns were written for. Adding a sample is dropping a file
// into the directory — no test code needs to change.
func TestTheVendoredCorpusTripsEveryAttackAndPassesEveryHonestAnswer(t *testing.T) {
	watched := inspector(t, domain.ModeMonitor)
	// The corpus payloads are assistant prose: the attacker's text as it would
	// reach the client in an ordinary chat answer.
	wrap := func(payload string) []byte {
		body, err := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": payload},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	scan := func(t *testing.T, path string) []domain.Finding {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		decision := watched.Inspect(wrap(string(raw)), false, application.Subject{})
		return decision.Findings
	}

	t.Run("malicious", func(t *testing.T) {
		paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "malicious", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			t.Fatal("the malicious corpus is empty — the vendored files are missing")
		}
		for _, path := range paths {
			path := path
			t.Run(filepath.Base(path), func(t *testing.T) {
				if findings := scan(t, path); len(findings) == 0 {
					t.Fatalf("malicious sample produced no findings (evasion): %s", filepath.Base(path))
				}
			})
		}
	})

	t.Run("clean", func(t *testing.T) {
		paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "clean", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			t.Fatal("the clean corpus is empty — the vendored files are missing")
		}
		for _, path := range paths {
			path := path
			t.Run(filepath.Base(path), func(t *testing.T) {
				if findings := scan(t, path); len(findings) != 0 {
					t.Fatalf("clean sample produced findings (false positive) in %s:\n  %+v", filepath.Base(path), findings)
				}
			})
		}
	})
}
