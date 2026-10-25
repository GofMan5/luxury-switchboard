//go:build windows

package dpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

func fixtureSet() domain.Set {
	return domain.Set{
		RequestRules: []domain.RequestRule{{
			ID: "fixture-route", Name: "Fixture route", Enabled: true,
			Match:   domain.RequestMatch{Models: []string{"fixture-*"}, Endpoints: []string{"chat"}},
			Effects: domain.Effects{Provider: "fixture-private-provider", Headers: map[string]string{"X-Fixture": "one"}},
		}},
		RetryRules: []domain.RetryRule{{
			ID: "fixture-retry", Name: "Fixture retry", Enabled: true, Owner: "plugin:fixture-plugin",
			Match:  domain.RetryMatch{Statuses: []int{503}, Contains: []string{}, ErrorCodes: []string{}, Providers: []string{}, Models: []string{}},
			Action: domain.RetryRetry, MaxAttempts: 2, DelayMS: 100,
		}},
	}.Normalized()
}

func TestRulesRoundTripIsEncrypted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.dpapi")
	repository := New(path)
	if _, found, err := repository.Load(context.Background()); err != nil || found {
		t.Fatalf("a missing file is not an empty store: %v %v", found, err)
	}
	rules := fixtureSet()
	if err := repository.Save(context.Background(), rules); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, magic) || bytes.Contains(raw, []byte("fixture-private-provider")) {
		t.Fatal("rules were stored as plaintext")
	}
	loaded, found, err := repository.Load(context.Background())
	if err != nil || !found || !reflect.DeepEqual(loaded.Normalized(), rules) {
		t.Fatalf("unexpected restored rules: %+v %v %v", loaded, found, err)
	}
}

func TestAnUnresolvedPathIsNotAnEmptyStore(t *testing.T) {
	repository := New("")
	if _, _, err := repository.Load(context.Background()); err == nil {
		t.Fatal("an unresolved path loaded as empty")
	}
	if repository.Save(context.Background(), fixtureSet()) == nil {
		t.Fatal("an unresolved path accepted a save")
	}
}

func TestACorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.dpapi")
	if err := os.WriteFile(path, append(append([]byte{}, magic...), []byte("not encrypted")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(path).Load(context.Background()); err == nil {
		t.Fatal("a corrupt rules file loaded")
	}
}
