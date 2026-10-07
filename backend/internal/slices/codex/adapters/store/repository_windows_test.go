//go:build windows

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// fixtureSession carries a value in every field of the domain type, so
// a round-trip that drops anything fails loudly. The values are
// fixtures, not credentials.
func fixtureSession() domain.Session {
	return domain.Session{
		AccessToken:  "fixture-access-token-value",
		RefreshToken: "fixture-refresh-token-value",
		IDToken:      "fixture-id-token-value",
		AccessExpiry: time.Date(2025, 6, 2, 12, 34, 56, 789123456, time.UTC),
		Identity: domain.Identity{
			Email:          "owner@example.invalid",
			ChatGPTUserID:  "fixture-chatgpt-user-id",
			Plan:           "team",
			AccountID:      "fixture-account-id",
			OrganizationID: "fixture-organization-id",
		},
	}
}

// writeProtected stores magic + DPAPI(plaintext) at path — the same
// framing a Save produces — so tests can hand-craft undecodable or
// future-versioned payloads.
func writeProtected(t *testing.T, path string, plaintext []byte) {
	t.Helper()
	protected, err := secretstore.Protect(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(append([]byte(nil), fileMagic...), protected...)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertSessionEqual compares every field of a restored session without
// echoing token material into the failure message.
func assertSessionEqual(t *testing.T, loaded, want domain.Session) {
	t.Helper()
	if loaded.AccessToken != want.AccessToken || loaded.RefreshToken != want.RefreshToken || loaded.IDToken != want.IDToken {
		t.Fatalf("restored tokens differ from the saved session: %s", loaded.RedactedSummary())
	}
	if !loaded.AccessExpiry.Equal(want.AccessExpiry) {
		t.Fatalf("restored access expiry differs from the saved session: %s", loaded.RedactedSummary())
	}
	if loaded.Identity != want.Identity {
		t.Fatalf("restored identity differs from the saved session: %s", loaded.RedactedSummary())
	}
}

func TestRepositoryRoundTripIsEncryptedAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	repository := NewRepository(path)
	want := fixtureSession()
	if err := repository.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, fileMagic) {
		t.Fatal("store file does not carry the codex magic prefix")
	}
	for _, forbidden := range [][]byte{
		[]byte(want.AccessToken),
		[]byte(want.RefreshToken),
		[]byte(want.IDToken),
	} {
		if bytes.Contains(raw, forbidden) {
			t.Fatal("encrypted repository contains plaintext")
		}
	}
	loaded, ok, err := repository.Load(context.Background())
	if err != nil || !ok {
		t.Fatalf("load after save failed: ok=%v err=%v", ok, err)
	}
	assertSessionEqual(t, loaded, want)

	want.AccessToken = "replacement-access-token-value"
	if err := repository.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err = repository.Load(context.Background())
	if err != nil || !ok || loaded.AccessToken != want.AccessToken {
		t.Fatalf("atomic replacement did not persist the update: ok=%v err=%v", ok, err)
	}
}

func TestClearRemovesTheStoredSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	repository := NewRepository(path)
	if err := repository.Save(context.Background(), fixtureSession()); err != nil {
		t.Fatal(err)
	}
	if err := repository.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear left the store file behind: %v", err)
	}
	loaded, ok, err := repository.Load(context.Background())
	if err != nil || ok || loaded != (domain.Session{}) {
		t.Fatalf("session survived clear: ok=%v err=%v", ok, err)
	}
}

func TestLoadRejectsCorruptedCiphertext(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	payload := append(append([]byte(nil), fileMagic...), []byte("bytes shaped like nothing DPAPI recognizes")...)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := NewRepository(path).Load(context.Background())
	requireStoreError(t, err)
}

func TestLoadRejectsUndecodablePayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	writeProtected(t, path, []byte("not session json"))
	_, _, err := NewRepository(path).Load(context.Background())
	requireStoreError(t, err)
}

func TestLoadRejectsUnexpectedVersions(t *testing.T) {
	for _, version := range []int{0, fileVersion + 1} {
		path := filepath.Join(t.TempDir(), fileName)
		plaintext, err := json.Marshal(document{Version: version, Session: fixtureSession()})
		if err != nil {
			t.Fatal(err)
		}
		writeProtected(t, path, plaintext)
		_, _, err = NewRepository(path).Load(context.Background())
		if err == nil {
			t.Fatalf("version %d was accepted", version)
		}
		requireStoreError(t, err)
	}
}

func TestSaveCreatesMissingParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", fileName)
	repository := NewRepository(path)
	if err := repository.Save(context.Background(), fixtureSession()); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := repository.Load(context.Background())
	if err != nil || !ok {
		t.Fatalf("load after save into fresh directories failed: ok=%v err=%v", ok, err)
	}
	assertSessionEqual(t, loaded, fixtureSession())
}
