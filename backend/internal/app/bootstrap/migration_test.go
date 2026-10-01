package bootstrap

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newLogger(into *strings.Builder) *log.Logger {
	return log.New(into, "", 0)
}

// A failure costs exactly the file it happened on, and a later run finishes
// what an earlier one left. The all-or-nothing publish this replaces stranded
// half a migration forever: anyExists saw the files that HAD landed and
// refused to run again, so the ones the same crash never wrote were never
// coming.
func TestTheLegacyImportCompletesAcrossRestarts(t *testing.T) {
	root := t.TempDir()
	providers := filepath.Join(root, "providers.dpapi")
	keys := filepath.Join(root, "keys.dpapi")
	logs := &strings.Builder{}
	logger := newLogger(logs)
	failing := true
	destinations := []migrationDestination{
		{providers, func(temp string) error {
			return os.WriteFile(temp, []byte("providers"), 0o600)
		}},
		{keys, func(temp string) error {
			if failing {
				return errors.New("injected write failure")
			}
			return os.WriteFile(temp, []byte("keys"), 0o600)
		}},
	}

	importEachMissing(logger, destinations)
	if content, err := os.ReadFile(providers); err != nil || string(content) != "providers" {
		t.Fatalf("the failure on one file cost the other: %v %q", err, content)
	}
	if _, err := os.Stat(keys); !os.IsNotExist(err) {
		t.Fatalf("a failed import left its file behind: %v", err)
	}
	if !strings.Contains(logs.String(), "partially imported") {
		t.Fatalf("the operator was not told the import is partial: %q", logs.String())
	}

	// The restart: providers already landed, the failing key gets another
	// chance, and only what is missing is attempted.
	failing = false
	logs.Reset()
	seen := 0
	attempted := []string{}
	track := []migrationDestination{
		{providers, func(temp string) error {
			seen++
			return os.WriteFile(temp, []byte("never"), 0o600)
		}},
		{keys, func(temp string) error {
			attempted = append(attempted, temp)
			return os.WriteFile(temp, []byte("keys"), 0o600)
		}},
	}
	importEachMissing(logger, track)
	if seen != 0 {
		t.Fatal("a landed file was rewritten by the retry")
	}
	if len(attempted) != 1 {
		t.Fatalf("the retry attempted %d files, want only the missing one", len(attempted))
	}
	if content, err := os.ReadFile(keys); err != nil || string(content) != "keys" {
		t.Fatalf("the retry did not land the missing file: %v %q", err, content)
	}
	if strings.Contains(logs.String(), "partially") {
		t.Fatalf("a completed retry reported itself partial: %q", logs.String())
	}
}
