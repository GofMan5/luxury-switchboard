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
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
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

// versionOneDocument is the pre-widening on-disk shape: version and
// session only, no login method, no reset counter. It exists so a
// tolerance test starts from bytes a real v1 install wrote, not from a
// v2 document that merely left two fields zero.
type versionOneDocument struct {
	Version int            `json:"version"`
	Session domain.Session `json:"session"`
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

// writeLegacyDocument stores a versioned document at the legacy
// single-session path, exactly as the pre-accounts store wrote it, so
// migration tests start from a realistic on-disk state.
func writeLegacyDocument(t *testing.T, repository *Repository, doc document) {
	t.Helper()
	plaintext, err := json.Marshal(doc)
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

// expectAccounts asserts the loaded slice matches want record-for-record
// in order — session, login method and reset counter together — so the
// deterministic load order and the v2 record both stay part of every
// round trip.
func expectAccounts(t *testing.T, loaded, want []application.StoredAccount) {
	t.Helper()
	if len(loaded) != len(want) {
		t.Fatalf("loaded %d accounts, want %d", len(loaded), len(want))
	}
	for i := range want {
		assertSessionEqual(t, loaded[i].Session, want[i].Session)
		if loaded[i].LoginMethod != want[i].LoginMethod {
			t.Fatalf("loaded login method %q, want %q", loaded[i].LoginMethod, want[i].LoginMethod)
		}
		if loaded[i].Resets != want[i].Resets {
			t.Fatalf("loaded reset count %d, want %d", loaded[i].Resets, want[i].Resets)
		}
	}
}

// accountPathOf sorts loaded records the way Load does: by the file
// name each record's session maps to.
func accountPathOf(repository *Repository, record application.StoredAccount) string {
	return repository.accountPath(record.Session)
}

func TestRepositoryRoundTripIsEncryptedAndAtomic(t *testing.T) {
	repository := storePaths(t)
	first, second := fixtureSession(), fixtureSecondSession()
	// The record is more than its session: the login method and the
	// reset counter ride the same file, so the round trip proves the
	// whole v2 record travels, not just the tokens.
	firstRecord := application.StoredAccount{Session: first, LoginMethod: application.LoginMethodBrowser, Resets: 3}
	secondRecord := application.StoredAccount{Session: second, LoginMethod: application.LoginMethodDevice, Resets: 0}
	if err := repository.Save(context.Background(), firstRecord); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(context.Background(), secondRecord); err != nil {
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
	ordered := []application.StoredAccount{firstRecord, secondRecord}
	sort.Slice(ordered, func(i, j int) bool {
		return accountPathOf(repository, ordered[i]) < accountPathOf(repository, ordered[j])
	})
	expectAccounts(t, loaded, ordered)

	// Re-saving an identity replaces exactly that account's file, and
	// the replacement is whole: a fresh record with a moved reset
	// counter must not find the old count merged back in.
	first.AccessToken = "replacement-access-token-value"
	replacement := application.StoredAccount{Session: first, LoginMethod: application.LoginMethodBrowser, Resets: 4}
	if err := repository.Save(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	loaded, err = repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ordered = []application.StoredAccount{replacement, secondRecord}
	sort.Slice(ordered, func(i, j int) bool {
		return accountPathOf(repository, ordered[i]) < accountPathOf(repository, ordered[j])
	})
	expectAccounts(t, loaded, ordered)
}

func TestRenamingAnAccountReplacesItsCredentialsAcrossRestartAndLogout(t *testing.T) {
	repository := storePaths(t)
	ctx := context.Background()
	original := application.StoredAccount{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser, Resets: 1}
	if err := repository.Save(ctx, original); err != nil {
		t.Fatal(err)
	}
	renamed := original
	renamed.Session.Identity.Email = "renamed@example.invalid"
	renamed.Session.Identity.OrganizationID = "renamed-fixture-organization"
	renamed.Session.AccessToken = "renamed-fixture-access"
	renamed.Session.RefreshToken = "renamed-fixture-refresh"
	renamed.LoginMethod = application.LoginMethodDevice
	renamed.Resets = 7
	if err := repository.Save(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	// A fresh repository uses only durable bytes, not a cached identity.
	restarted := NewRepository(repository.dir, repository.legacyPath)
	loaded, err := restarted.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{renamed})
	// Even a caller still holding the old email/org must clear this ID.
	if err := restarted.Clear(ctx, original.Session); err != nil {
		t.Fatal(err)
	}
	loaded, err = NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, nil)
}

// writeOldAccount recreates the email/account/org filename used before F19.
// mtime is explicit: token expiry is not credential freshness.
func writeOldAccount(t *testing.T, repository *Repository, stored application.StoredAccount, modified time.Time) string {
	t.Helper()
	prepareDir(t, repository)
	path := filepath.Join(repository.dir, domain.StorageID(stored.Session.Identity.Email, stored.Session.Identity.AccountID, stored.Session.Identity.OrganizationID)+".dpapi")
	plaintext, err := json.Marshal(documentFromStored(stored))
	if err != nil {
		t.Fatal(err)
	}
	writeProtected(t, path, plaintext)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLegacyAccountCopiesMigrateToTheLatestRecordAndDoNotReturnAfterLogout(t *testing.T) {
	repository := storePaths(t)
	ctx := context.Background()
	old := application.StoredAccount{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser, Resets: 2}
	latest := old
	latest.Session.Identity.Email = "renamed@example.invalid"
	latest.Session.Identity.OrganizationID = "renamed-fixture-org"
	latest.Session.AccessToken = "latest-fixture-access"
	latest.Session.RefreshToken = "latest-fixture-refresh"
	latest.Session.AccessExpiry = old.Session.AccessExpiry.Add(-time.Hour)
	latest.LoginMethod = application.LoginMethodDevice
	latest.Resets = 9
	modified := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	oldPath := writeOldAccount(t, repository, old, modified)
	latestPath := writeOldAccount(t, repository, latest, modified.Add(time.Hour))
	// The ignored pre-plural singleton must never become active when
	// specific logout makes the account directory empty again.
	writeLegacyDocument(t, repository, documentFromStored(old))
	loaded, err := NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{latest})
	for _, path := range []string{oldPath, latestPath, repository.legacyPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("migration retained a superseded credential file")
		}
	}
	loaded, err = NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{latest})
	if err := repository.Clear(ctx, latest.Session); err != nil {
		t.Fatal(err)
	}
	loaded, err = NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, nil)
}

func TestSaveRetiresOldFilenamesWithoutMergingDistinctAccountIDs(t *testing.T) {
	repository := storePaths(t)
	ctx := context.Background()
	old := application.StoredAccount{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser}
	modified := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	oldPath := writeOldAccount(t, repository, old, modified)
	variant := old
	variant.Session.Identity.Email = "previous-name@example.invalid"
	variantPath := writeOldAccount(t, repository, variant, modified.Add(time.Hour))
	// Equal email, organization and user ID are not grounds to collapse
	// two legitimate exact account IDs (including case differences).
	other := old
	other.Session.Identity.AccountID = "Fixture-account-id"
	other.Session.RefreshToken = "distinct-fixture-refresh"
	other.LoginMethod = application.LoginMethodDevice
	writeOldAccount(t, repository, other, modified)
	latest := variant
	latest.Session.Identity.OrganizationID = "current-fixture-org"
	latest.Session.RefreshToken = "current-fixture-refresh"
	latest.Resets = 12
	if err := repository.Save(ctx, latest); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{oldPath, variantPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("save retained an old credential filename")
		}
	}
	loaded, err := NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []application.StoredAccount{latest, other}
	sort.Slice(want, func(i, j int) bool { return accountPathOf(repository, want[i]) < accountPathOf(repository, want[j]) })
	expectAccounts(t, loaded, want)
	if err := repository.Clear(ctx, old.Session); err != nil {
		t.Fatal(err)
	}
	loaded, err = NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{other})
}

func TestClearBeforeMigrationRemovesEveryCopyButPreservesADistinctID(t *testing.T) {
	repository := storePaths(t)
	ctx := context.Background()
	old := application.StoredAccount{Session: fixtureSession()}
	modified := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	writeOldAccount(t, repository, old, modified)
	variant := old
	variant.Session.Identity.Email = "previous-name@example.invalid"
	variant.Session.Identity.OrganizationID = "previous-fixture-org"
	writeOldAccount(t, repository, variant, modified.Add(time.Hour))
	other := application.StoredAccount{Session: fixtureSecondSession(), Resets: 3}
	writeOldAccount(t, repository, other, modified)
	writeLegacyDocument(t, repository, documentFromStored(old))
	if err := NewRepository(repository.dir, repository.legacyPath).Clear(ctx, variant.Session); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{other})
	if err := repository.ClearAll(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err = NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, nil)
}

func TestClearOfOnlyASingletonMatchesExactIDRegardlessOfEmail(t *testing.T) {
	repository := storePaths(t)
	ctx := context.Background()
	stored := application.StoredAccount{Session: fixtureSession(), Resets: 4}
	writeLegacyDocument(t, repository, documentFromStored(stored))
	if err := repository.Clear(ctx, fixtureSecondSession()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repository.legacyPath); err != nil {
		t.Fatal("specific clear removed a distinct singleton identity")
	}
	renamed := stored.Session
	renamed.Identity.Email = "renamed@example.invalid"
	renamed.Identity.OrganizationID = "changed-fixture-org"
	if err := repository.Clear(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, nil)
}

func TestEqualTimestampCopiesPreferTheAlreadyStableRecord(t *testing.T) {
	repository := storePaths(t)
	ctx := context.Background()
	current := application.StoredAccount{Session: fixtureSession(), Resets: 8}
	if err := repository.Save(ctx, current); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	stablePath := repository.accountPath(current.Session)
	if err := os.Chtimes(stablePath, modified, modified); err != nil {
		t.Fatal(err)
	}
	old := current
	old.Session.Identity.Email = "previous-name@example.invalid"
	old.Session.RefreshToken = "old-fixture-refresh"
	old.Resets = 1
	writeOldAccount(t, repository, old, modified)
	loaded, err := NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{current})
}

func TestAValidOlderCopySurvivesANewerEmptySessionCopy(t *testing.T) {
	repository := storePaths(t)
	ctx := context.Background()
	valid := application.StoredAccount{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser, Resets: 6}
	modified := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	writeOldAccount(t, repository, valid, modified)
	empty := valid
	empty.Session.Identity.Email = "empty-copy@example.invalid"
	empty.Session.AccessToken = ""
	empty.Session.RefreshToken = ""
	empty.Session.IDToken = ""
	writeOldAccount(t, repository, empty, modified.Add(time.Hour))
	loaded, err := repository.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{valid})
	loaded, err = NewRepository(repository.dir, repository.legacyPath).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{valid})
}

func TestConcurrentLoadAndClearCannotRecreateALegacyAccount(t *testing.T) {
	ctx := context.Background()
	for range 8 {
		repository := storePaths(t)
		stored := application.StoredAccount{Session: fixtureSession()}
		writeOldAccount(t, repository, stored, time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC))
		start := make(chan struct{})
		outcomes := make(chan error, 2)
		go func() {
			<-start
			_, err := repository.Load(ctx)
			outcomes <- err
		}()
		go func() {
			<-start
			outcomes <- repository.Clear(ctx, stored.Session)
		}()
		close(start)
		for range 2 {
			if err := <-outcomes; err != nil {
				t.Fatal(err)
			}
		}
		loaded, err := NewRepository(repository.dir, repository.legacyPath).Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		expectAccounts(t, loaded, nil)
	}
}

func TestClearRemovesOnlyThatAccount(t *testing.T) {
	repository := storePaths(t)
	first, second := fixtureSession(), fixtureSecondSession()
	if err := repository.Save(context.Background(), application.StoredAccount{Session: first, LoginMethod: application.LoginMethodBrowser}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(context.Background(), application.StoredAccount{Session: second, LoginMethod: application.LoginMethodDevice, Resets: 1}); err != nil {
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
	expectAccounts(t, loaded, []application.StoredAccount{{Session: second, LoginMethod: application.LoginMethodDevice, Resets: 1}})
}

func TestClearAllRemovesEveryAccountAndTheLegacyFile(t *testing.T) {
	repository := storePaths(t)
	if err := repository.Save(context.Background(), application.StoredAccount{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(context.Background(), application.StoredAccount{Session: fixtureSecondSession(), LoginMethod: application.LoginMethodDevice}); err != nil {
		t.Fatal(err)
	}
	writeLegacyDocument(t, repository, document{Version: fileVersion, Session: fixtureSession()})
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

func TestVersionOneFilesAreReadAsUnknownMethodAndZeroResets(t *testing.T) {
	// A sign-in must not be lost to a schema widening: v1 files predate
	// the login method and the reset counter, so Load keeps the session
	// and answers the fields it never had as zero values. The file is
	// written in the exact v1 shape — no loginMethod, no resets key —
	// because a v2 document with zeroed fields would prove nothing
	// about the old bytes a real install wrote.
	repository := storePaths(t)
	prepareDir(t, repository)
	plaintext, err := json.Marshal(versionOneDocument{Version: 1, Session: fixtureSession()})
	if err != nil {
		t.Fatal(err)
	}
	writeProtected(t, filepath.Join(repository.dir, "codex_v1.dpapi"), plaintext)
	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatalf("a version 1 account file must still load: %v", err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{{Session: fixtureSession(), LoginMethod: "", Resets: 0}})
}

func TestSaveCreatesMissingParentDirectories(t *testing.T) {
	base := t.TempDir()
	repository := NewRepository(filepath.Join(base, "nested", "deeper", accountsDirName), filepath.Join(base, legacyFileName))
	if err := repository.Save(context.Background(), application.StoredAccount{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser, Resets: 2}); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser, Resets: 2}})
}

func TestLegacySessionIsMigratedIntoTheAccountDirectory(t *testing.T) {
	repository := storePaths(t)
	// The legacy store learned the login method and reset counter before
	// it was retired, so the migration has to carry the whole record —
	// not just the session — into the account directory.
	want := application.StoredAccount{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser, Resets: 5}
	writeLegacyDocument(t, repository, document{
		Version:     fileVersion,
		Session:     want.Session,
		LoginMethod: string(want.LoginMethod),
		Resets:      want.Resets,
	})

	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{want})

	// The migration is durable: the account file exists, the legacy
	// file is gone, and a second load answers from the directory.
	if _, err := os.Stat(repository.accountPath(want.Session)); err != nil {
		t.Fatalf("migration did not write the account file: %v", err)
	}
	if _, err := os.Stat(repository.legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration left the legacy file behind: %v", err)
	}
	again, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, again, []application.StoredAccount{want})
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

func TestLegacyFileIsRetiredWhenAccountsExist(t *testing.T) {
	repository := storePaths(t)
	want := application.StoredAccount{Session: fixtureSession(), LoginMethod: application.LoginMethodBrowser, Resets: 1}
	if err := repository.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	writeLegacyDocument(t, repository, document{Version: fileVersion, Session: fixtureSecondSession()})

	loaded, err := repository.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectAccounts(t, loaded, []application.StoredAccount{want})
	if _, err := os.Stat(repository.legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load must retire the legacy file once accounts exist: %v", err)
	}
}
