//go:build linux

package dpapi

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
	"github.com/zalando/go-keyring"
)

func TestRulesRoundTripEncryptedOnLinux(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "rules.dpapi")
	repository := New(path)
	rules := domain.Set{
		RequestRules: []domain.RequestRule{{
			ID: "fixture-route", Name: "Fixture route", Enabled: true,
			Effects: domain.Effects{Provider: "fixture-private-provider"},
		}},
	}.Normalized()
	if err := repository.Save(context.Background(), rules); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("fixture-private-provider")) {
		t.Fatal("rules stored as plaintext")
	}
	loaded, found, err := repository.Load(context.Background())
	if err != nil || !found || !reflect.DeepEqual(loaded.Normalized(), rules) {
		t.Fatalf("unexpected rules: %+v %v %v", loaded, found, err)
	}
}
