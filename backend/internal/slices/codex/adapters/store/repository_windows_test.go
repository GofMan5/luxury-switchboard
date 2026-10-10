//go:build windows

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
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

// fixtureSecondSession differs from fixtureSession in every identity
// field, so the two accounts never share a storage key.
func fixtureSecondSession() domain.Session {
	session := fixtureSession()
	session.AccessToken = "second-fixture-access-token-value"
	session.RefreshToken = "second-fixture-refresh-token-value"
	session.IDToken = "second-fixture-id-token-value"
	session.Identity = domain.Identity{
		Email:          "other@example.invalid",
		ChatGPTUserID:  "second-fixture-chatgpt-user-id",
		Plan:           "plus",
		AccountID:      "second-fixture-account-id",
		OrganizationID: "second-fixture-organization-id",
	}
	return session
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

// writeLegacyDocument stores a versioned session document at the
// legacy single-session path, exactly as the pre-accounts store wrote
// it, so migration tests start from a realistic on-disk state.
func writeLegacyDocument(t *testing.T, repository *Repository, session domain.Session) {
	t.Helper()
	plaintext, err := json.Marshal(document{Version: fileVersion, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	writeProtected(t, repository.legacyPath, plaintext)
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

// expectSessions asserts the loaded slice matches want session-for-
// session in order, so the deterministic load order is part of every
// round trip.
func expectSessions(t *testing.T, loaded, want []domain.Session) {
	t.Helper()
	if len(loaded) != len(want) {
		t.Fatalf("loaded %d accounts, want %d", len(loaded), len(want))
	}
	for i := range want {
		assertSessionEqual(t, loaded[i], want[i])
	}
}

func TestRepositoryRoundTripIsEncryptedAndAtomic(t *testing.T) {
	repository := storePaths(t)
	first, second := fixtureSession(), fixtureSecondSession()
	if err := repository.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	for _, session := range []domain.Session{first, second} {
		raw, err := os.ReadFile(repository.accountPath(session))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(raw, fileMagic) {
			t.Fatal("store file does not carry the codex magic prefix")
		}
		for _, forbidden := range [][]byte{
			[]byte(session.AccessToken),
			[]byte(session.RefreshToken),
			[]byte(session.IDToken),
		} {
			if bytes.Contains(raw, forbidden) {
				t.Fatal("encrypted repository contains plaintext")
			}
		}
	}
	// The load order is the sorted file-name order, so it is stable
	// across runs regardless of directory enumeration.
	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ordered := []domain.Session{first, second}
	sort.Slice(ordered, func(i, j int) bool {
		return repository.accountPath(ordered[i]) < repository.accountPath(ordered[j])
	})
	expectSessions(t, loaded, ordered)

	// Re-saving an identity replaces exactly that account's file.
	first.AccessToken = "replacement-access-token-value"
	if err := repository.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ordered = []domain.Session{first, second}
	sort.Slice(ordered, func(i, j int) bool {
		return repository.accountPath(ordered[i]) < repository.accountPath(ordered[j])
	})
	expectSessions(t, loaded, ordered)
}

func TestClearRemovesOnlyThatAccount(t *testing.T) {
	repository := storePaths(t)
	first, second := fixtureSession(), fixtureSecondSession()
	if err := repository.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := repository.Clear(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repository.accountPath(first)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear left the removed account's file behind: %v", err)
	}
	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectSessions(t, loaded, []domain.Session{second})
}

func TestClearAllRemovesEveryAccountAndTheLegacyFile(t *testing.T) {
	repository := storePaths(t)
	if err := repository.Save(context.Background(), fixtureSession()); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(context.Background(), fixtureSecondSession()); err != nil {
		t.Fatal(err)
	}
	writeLegacyDocument(t, repository, fixtureSession())
	if err := repository.ClearAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(repository.dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("clear all left accounts behind: entries=%d err=%v", len(entries), err)
	}
	if _, err := os.Stat(repository.legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear all left the legacy file behind: %v", err)
	}
}

func TestLoadRejectsCorruptedCiphertext(t *testing.T) {
	repository := storePaths(t)
	prepareDir(t, repository)
	payload := append(append([]byte(nil), fileMagic...), []byte("bytes shaped like nothing DPAPI recognizes")...)
	if err := os.WriteFile(filepath.Join(repository.dir, "codex_corrupt.dpapi"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := repository.Load(context.Background())
	requireStoreError(t, err)
}

func TestLoadRejectsUndecodablePayload(t *testing.T) {
	repository := storePaths(t)
	prepareDir(t, repository)
	writeProtected(t, filepath.Join(repository.dir, "codex_undecodable.dpapi"), []byte("not session json"))
	_, err := repository.Load(context.Background())
	requireStoreError(t, err)
}

func TestLoadRejectsUnexpectedVersions(t *testing.T) {
	for _, version := range []int{0, fileVersion + 1} {
		repository := storePaths(t)
		prepareDir(t, repository)
		plaintext, err := json.Marshal(document{Version: version, Session: fixtureSession()})
		if err != nil {
			t.Fatal(err)
		}
		writeProtected(t, filepath.Join(repository.dir, "codex_versioned.dpapi"), plaintext)
		_, err = repository.Load(context.Background())
		if err == nil {
			t.Fatalf("version %d was accepted", version)
		}
		requireStoreError(t, err)
	}
}

func TestSaveCreatesMissingParentDirectories(t *testing.T) {
	base := t.TempDir()
	repository := NewRepository(filepath.Join(base, "nested", "deeper", accountsDirName), filepath.Join(base, legacyFileName))
	if err := repository.Save(context.Background(), fixtureSession()); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectSessions(t, loaded, []domain.Session{fixtureSession()})
}

func TestLegacySessionIsMigratedIntoTheAccountDirectory(t *testing.T) {
	repository := storePaths(t)
	want := fixtureSession()
	writeLegacyDocument(t, repository, want)

	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectSessions(t, loaded, []domain.Session{want})

	// The migration is durable: the account file exists, the legacy
	// file is gone, and a second load answers from the directory.
	if _, err := os.Stat(repository.accountPath(want)); err != nil {
		t.Fatalf("migration did not write the account file: %v", err)
	}
	if _, err := os.Stat(repository.legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration left the legacy file behind: %v", err)
	}
	again, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectSessions(t, again, []domain.Session{want})
}

func TestUnreadableLegacyFileIsAnError(t *testing.T) {
	repository := storePaths(t)
	payload := append(append([]byte(nil), fileMagic...), []byte("bytes shaped like nothing DPAPI recognizes")...)
	if err := os.WriteFile(repository.legacyPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	// Silently dropping the user's sign-in would be worse than
	// refusing to start: the account stays on disk until it is
	// readable again.
	_, err := repository.Load(context.Background())
	requireStoreError(t, err)
}

func TestLegacyFileIsIgnoredWhenAccountsExist(t *testing.T) {
	repository := storePaths(t)
	want := fixtureSession()
	if err := repository.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	writeLegacyDocument(t, repository, fixtureSecondSession())

	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectSessions(t, loaded, []domain.Session{want})
	if _, err := os.Stat(repository.legacyPath); err != nil {
		t.Fatalf("load must not touch the legacy file once accounts exist: %v", err)
	}
}
