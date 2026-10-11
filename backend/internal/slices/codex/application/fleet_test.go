package application

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// laterPrimaryWindow answers the same plan with a later primary reset:
// the shape the next limit cycle produces. The secondary window is
// deliberately untouched — the reset counter follows the primary cycle,
// and a test that moved both could not tell which one was counted.
func laterPrimaryWindow() domain.Usage {
	usage := sampleUsage()
	usage.Primary.ResetAt = time.Unix(1_800_100_000, 0).UTC()
	return usage
}

// unrefreshableSession is a stored session the acquire path cannot
// renew: the refresh token is gone and the access token already
// expired. Restore keeps the row signed in, and the first acquire
// reclassifies it to reauth-needed without any network round-trip —
// the deterministic setup for the states wake and the fleet check
// must answer honestly.
func unrefreshableSession(accountID, email string) domain.Session {
	session := accountSession(accountID, email)
	session.RefreshToken = ""
	session.AccessExpiry = time.Now().Add(-10 * time.Minute)
	return session
}

// wakeOutcome finds one account's outcome by id: fleet answers are
// addressed by identity, never by position.
func wakeOutcome(t *testing.T, outcomes []WakeOutcome, accountID string) WakeOutcome {
	t.Helper()
	for _, outcome := range outcomes {
		if outcome.AccountID == accountID {
			return outcome
		}
	}
	t.Fatalf("wake outcomes have no %q: %+v", accountID, outcomes)
	return WakeOutcome{}
}

// quotaOutcome finds one account's quota outcome by id, for the same
// reason.
func quotaOutcome(t *testing.T, outcomes []QuotaOutcome, accountID string) QuotaOutcome {
	t.Helper()
	for _, outcome := range outcomes {
		if outcome.AccountID == accountID {
			return outcome
		}
	}
	t.Fatalf("quota outcomes have no %q: %+v", accountID, outcomes)
	return QuotaOutcome{}
}

// A later primary window is one spent cycle, and the counter the owner
// watches must charge exactly that. The baseline probe counts nothing
// (no prior window to compare), the first rotation increments and
// persists the account's facts, the same window again does not
// double-count, and the row change is announced — the counter lives on
// the account's row, not only on its card.
func TestARotationIsCountedOnceAndPersisted(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	savesBefore := len(env.store.savedAccounts())
	eventsBefore := len(env.events.snapshots())

	env.authorizer.setUsageResult(sampleUsage(), nil)
	env.service.RefreshQuota(context.Background(), "account-1")
	if row := accountStatus(t, env.service.Status(), "account-1"); row.Resets != 0 {
		t.Fatalf("baseline probe set Resets = %d, want 0", row.Resets)
	}
	if saves := env.store.savedAccounts(); len(saves) != savesBefore {
		t.Fatalf("baseline probe saved %d records, want none beyond the login's %d", len(saves), savesBefore)
	}
	if events := env.events.snapshots(); len(events) != eventsBefore {
		t.Fatalf("baseline probe fired %d events, want 0: the quota card is a pull", len(events)-eventsBefore)
	}

	env.authorizer.setUsageResult(laterPrimaryWindow(), nil)
	snapshot := env.service.RefreshQuota(context.Background(), "account-1")
	if snapshot.Err != "" {
		t.Fatalf("rotation probe reported %q, want none", snapshot.Err)
	}
	if row := accountStatus(t, env.service.Status(), "account-1"); row.Resets != 1 {
		t.Fatalf("rotation set Resets = %d, want 1", row.Resets)
	}
	saves := env.store.savedAccounts()
	if len(saves) != savesBefore+1 {
		t.Fatalf("rotation saved %d records, want %d+1", len(saves), savesBefore)
	}
	last := saves[len(saves)-1]
	if last.LoginMethod != LoginMethodBrowser || last.Resets != 1 {
		t.Fatalf("rotation persisted %+v, want method browser and resets 1", last)
	}
	if last.Session.Identity.AccountID != "account-1" {
		t.Fatalf("rotation persisted account %q, want account-1", last.Session.Identity.AccountID)
	}
	if events := env.events.snapshots(); len(events) != eventsBefore+1 {
		t.Fatalf("rotation fired %d events, want exactly 1: the counter is row state", len(events)-eventsBefore)
	}

	env.service.RefreshQuota(context.Background(), "account-1")
	if row := accountStatus(t, env.service.Status(), "account-1"); row.Resets != 1 {
		t.Fatalf("same window again set Resets = %d, want it kept at 1", row.Resets)
	}
	if saves := env.store.savedAccounts(); len(saves) != savesBefore+1 {
		t.Fatalf("same window again saved %d records, want no new save", len(saves))
	}
}

// A failed probe keeps the last good usage, so a rotation that lands
// across an outage is still detected — the counter cannot miss a spent
// cycle just because the upstream was unavailable when the owner asked.
// The outage itself counts nothing.
func TestARotationDetectedAfterAnOutageIsStillCountedOnce(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)

	env.authorizer.setUsageResult(sampleUsage(), nil)
	env.service.RefreshQuota(context.Background(), "account-1")

	env.authorizer.setUsageResult(domain.Usage{}, errors.New("503 service unavailable"))
	failed := env.service.RefreshQuota(context.Background(), "account-1")
	if failed.Err == "" {
		t.Fatal("the outage probe reported no error")
	}
	if failed.Usage != sampleUsage() {
		t.Fatalf("failed probe kept %+v, want the last good usage", failed.Usage)
	}
	if row := accountStatus(t, env.service.Status(), "account-1"); row.Resets != 0 {
		t.Fatalf("outage counted %d rotations, want 0", row.Resets)
	}

	env.authorizer.setUsageResult(laterPrimaryWindow(), nil)
	env.service.RefreshQuota(context.Background(), "account-1")
	if row := accountStatus(t, env.service.Status(), "account-1"); row.Resets != 1 {
		t.Fatalf("rotation after outage set Resets = %d, want 1", row.Resets)
	}
	if probes := env.authorizer.usageProbeCount(); probes != 3 {
		t.Fatalf("usage probed %d times, want 3", probes)
	}
}

// Restore is where stored method and counter facts must survive: a
// record saved with them reappears with them, and a bare session —
// what an older build or a hand-placed record means on disk — reads as
// methodless and uncounted.
func TestRestoredAccountsCarryTheirMethodAndResetCount(t *testing.T) {
	env := newTestEnv(t)
	env.store.seedStored(StoredAccount{
		Session:     accountSession("account-9", "nine@example.com"),
		LoginMethod: LoginMethodDevice,
		Resets:      4,
	})
	env.store.seed(accountSession("account-10", "ten@example.com"))
	env.restore(t)

	status := env.service.Status()
	if len(status.Accounts) != 2 {
		t.Fatalf("Status().Accounts has %d rows, want 2", len(status.Accounts))
	}
	recorded := accountStatus(t, status, "account-9")
	if recorded.LoginMethod != string(LoginMethodDevice) || recorded.Resets != 4 {
		t.Fatalf("account-9 row = %+v, want method device and resets 4", recorded)
	}
	if recorded.State != StateSignedIn {
		t.Fatalf("account-9 state = %q, want signed in", recorded.State)
	}
	bare := accountStatus(t, status, "account-10")
	if bare.LoginMethod != "" || bare.Resets != 0 {
		t.Fatalf("account-10 row = %+v, want no method and no resets", bare)
	}
}

// A re-login of the same identity replaces the method — the account
// was this time signed in by browser — and keeps the cycles it already
// burned through: the counter is the account's history, not a property
// of whichever session currently serves it.
func TestASignInReplacesTheMethodAndKeepsTheResetCount(t *testing.T) {
	env := newTestEnv(t)
	env.store.seedStored(StoredAccount{
		Session:     validSession("user@example.com"),
		LoginMethod: LoginMethodDevice,
		Resets:      3,
	})
	env.restore(t)

	env.signIn(t)

	row := accountStatus(t, env.service.Status(), "account-1")
	if row.LoginMethod != string(LoginMethodBrowser) {
		t.Fatalf("re-login left method %q, want browser", row.LoginMethod)
	}
	if row.Resets != 3 {
		t.Fatalf("re-login set Resets = %d, want the kept 3", row.Resets)
	}
	saves := env.store.savedAccounts()
	if len(saves) == 0 {
		t.Fatal("the re-login saved nothing")
	}
	last := saves[len(saves)-1]
	if last.LoginMethod != LoginMethodBrowser || last.Resets != 3 {
		t.Fatalf("re-login persisted %+v, want method browser and resets 3", last)
	}
}

// A token rotation persists the refreshed session together with the
// account facts it must carry: a save that dropped the method or the
// reset count would erase them from disk while memory kept them — the
// next restart would silently reset both.
func TestARefreshRotationKeepsTheMethodAndResetCount(t *testing.T) {
	env := newTestEnv(t)
	env.store.seedStored(StoredAccount{
		Session:     staleSession(),
		LoginMethod: LoginMethodImport,
		Resets:      2,
	})
	env.restore(t)

	rotated := domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}
	env.authorizer.setRefreshResult(rotated, nil)

	token, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("AcquireAccessToken() failed: %v", err)
	}
	if token != "rotated-access" {
		t.Fatalf("AcquireAccessToken() = %q, want rotated-access", token)
	}
	saves := env.store.savedAccounts()
	if len(saves) == 0 {
		t.Fatal("the rotation saved nothing")
	}
	last := saves[len(saves)-1]
	if last.Session.AccessToken != "rotated-access" {
		t.Fatalf("rotation persisted token %q, want rotated-access", last.Session.AccessToken)
	}
	if last.Session.Identity.Email != "user@example.com" {
		t.Fatalf("rotation lost the merged identity: %q", last.Session.Identity.Email)
	}
	if last.LoginMethod != LoginMethodImport || last.Resets != 2 {
		t.Fatalf("rotation persisted %+v, want method import and resets 2", last)
	}
}

// Waking an id that matches no account is the acquire path's refusal,
// not a command error: one outcome says the account is not signed in,
// and no network traffic is spent finding that out. An empty fleet
// asked to wake everything answers nothing.
func TestAWakeOfAnUnknownAccountSaysSoWithoutTheNetwork(t *testing.T) {
	env := newTestEnv(t)

	outcomes := env.service.Wake(context.Background(), "account-99")
	if len(outcomes) != 1 {
		t.Fatalf("Wake of an unknown id returned %d outcomes, want 1: %+v", len(outcomes), outcomes)
	}
	outcome := outcomes[0]
	if outcome.AccountID != "account-99" || outcome.State != StateSignedOut || outcome.Err != errNotSignedIn.Error() {
		t.Fatalf("unknown-id outcome = %+v, want not-signed-in refusal", outcome)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 0 {
		t.Fatalf("unknown-id wake refreshed %d times, want 0", len(refreshes))
	}
	if probes := env.authorizer.usageProbeCount(); probes != 0 {
		t.Fatalf("unknown-id wake probed usage %d times, want 0", probes)
	}
	if got := env.service.Wake(context.Background(), ""); len(got) != 0 {
		t.Fatalf("Wake of an empty fleet returned %d outcomes, want 0", len(got))
	}
}

// A reauth-needed account is not retried by a wake: its refresh token
// already failed once, so the honest answer is "sign in again",
// delivered without a round-trip the upstream is guaranteed to refuse.
func TestAWakeOfAnAccountThatNeedsSignInRefusesBeforeTheNetwork(t *testing.T) {
	env := newTestEnv(t)
	env.store.seed(unrefreshableSession("account-1", "user@example.com"))
	env.restore(t)

	if _, err := env.service.AcquireAccessToken(context.Background(), "account-1"); !errors.Is(err, errNeedsSignIn) {
		t.Fatalf("setup acquire failed with %v, want errNeedsSignIn", err)
	}

	outcomes := env.service.Wake(context.Background(), "account-1")
	if len(outcomes) != 1 {
		t.Fatalf("Wake returned %d outcomes, want 1: %+v", len(outcomes), outcomes)
	}
	outcome := outcomes[0]
	if outcome.AccountID != "account-1" || outcome.State != StateReauthNeeded || outcome.Err != errNeedsSignIn.Error() {
		t.Fatalf("reauth outcome = %+v, want reauth-needed refusal", outcome)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 0 {
		t.Fatalf("reauth wake refreshed %d times, want 0", len(refreshes))
	}
	if probes := env.authorizer.usageProbeCount(); probes != 0 {
		t.Fatalf("reauth wake probed usage %d times, want 0", probes)
	}
}

// Waking a healthy account forces the rotation the owner asked for:
// the old refresh token is spent upstream, the refreshed one is served
// from then on, and the outcome reports the account still signed in.
func TestAWakeOfASignedInAccountRotatesItsToken(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)

	rotated := domain.Session{
		AccessToken:  "rotated-access",
		RefreshToken: "rotated-refresh",
		AccessExpiry: time.Now().Add(time.Hour),
	}
	env.authorizer.setRefreshResult(rotated, nil)

	outcomes := env.service.Wake(context.Background(), "account-1")
	if len(outcomes) != 1 {
		t.Fatalf("Wake returned %d outcomes, want 1: %+v", len(outcomes), outcomes)
	}
	outcome := outcomes[0]
	if outcome.AccountID != "account-1" || outcome.State != StateSignedIn || outcome.Err != "" {
		t.Fatalf("healthy outcome = %+v, want signed in with no error", outcome)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 || refreshes[0] != "refresh-user@example.com" {
		t.Fatalf("wake refreshed %v, want the account's refresh token once", refreshes)
	}

	served, err := env.service.AcquireAccessToken(context.Background(), "account-1")
	if err != nil {
		t.Fatalf("acquire after wake failed: %v", err)
	}
	if served != "rotated-access" {
		t.Fatalf("acquire after wake served %q, want the rotated token", served)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 {
		t.Fatalf("acquire after wake refreshed again: %v", refreshes)
	}
}

// A wake that spends a dead refresh token reports the refresh failure
// itself — the owner must see why the account went dark, not a bare
// verdict — and the account is left reauth-needed, exactly as the
// acquire path classifies it.
func TestAWakeOfADeadRefreshTokenReportsTheRefreshFailure(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(domain.Session{}, errors.New("invalid_grant"))

	outcomes := env.service.Wake(context.Background(), "account-1")
	if len(outcomes) != 1 {
		t.Fatalf("Wake returned %d outcomes, want 1: %+v", len(outcomes), outcomes)
	}
	outcome := outcomes[0]
	if outcome.State != StateReauthNeeded {
		t.Fatalf("dead-token outcome state = %q, want reauth-needed", outcome.State)
	}
	want := "codex access token could not be refreshed: invalid_grant"
	if outcome.Err != want {
		t.Fatalf("dead-token outcome error = %q, want %q", outcome.Err, want)
	}
}

// Waking the fleet is one answer per account in the order the status
// list renders: the healthy account rotates, the broken one refuses
// before the network, and neither failure hides the other.
func TestAWakeOfTheFleetAnswersEveryAccountWithItsOwnOutcome(t *testing.T) {
	env := newTestEnv(t)
	env.store.seed(unrefreshableSession("account-2", "second@example.com"))
	env.restore(t)
	if _, err := env.service.AcquireAccessToken(context.Background(), "account-2"); !errors.Is(err, errNeedsSignIn) {
		t.Fatalf("setup acquire failed with %v, want errNeedsSignIn", err)
	}
	env.signIn(t)

	outcomes := env.service.Wake(context.Background(), "")
	if len(outcomes) != 2 {
		t.Fatalf("fleet wake returned %d outcomes, want 2: %+v", len(outcomes), outcomes)
	}
	if outcomes[0].AccountID != "account-1" || outcomes[1].AccountID != "account-2" {
		t.Fatalf("fleet wake order = %v, want sorted account-1, account-2", outcomes)
	}
	first := wakeOutcome(t, outcomes, "account-1")
	if first.State != StateSignedIn || first.Err != "" {
		t.Fatalf("account-1 outcome = %+v, want signed in with no error", first)
	}
	second := wakeOutcome(t, outcomes, "account-2")
	if second.State != StateReauthNeeded || second.Err != errNeedsSignIn.Error() {
		t.Fatalf("account-2 outcome = %+v, want reauth-needed refusal", second)
	}
	if refreshes := env.authorizer.refreshes(); len(refreshes) != 1 || refreshes[0] != "refresh-user@example.com" {
		t.Fatalf("fleet wake refreshed %v, want only the healthy account once", refreshes)
	}
}

// A fleet check probes every account once, answers in the order the
// status list renders, and each card carries its own snapshot — the
// command-level answer is the list, never one aggregate verdict.
func TestAFleetQuotaCheckProbesEveryAccountOnce(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	env.authorizer.setUsageResult(sampleUsage(), nil)
	outcomes := env.service.RefreshQuotaAll(context.Background())
	if len(outcomes) != 2 {
		t.Fatalf("RefreshQuotaAll returned %d outcomes, want 2: %+v", len(outcomes), outcomes)
	}
	if outcomes[0].AccountID != "account-1" || outcomes[1].AccountID != "account-2" {
		t.Fatalf("RefreshQuotaAll order = %v, want sorted account-1, account-2", outcomes)
	}
	for _, outcome := range outcomes {
		if outcome.Snapshot.Err != "" {
			t.Fatalf("outcome for %s reported %q, want none", outcome.AccountID, outcome.Snapshot.Err)
		}
		if outcome.Snapshot.Usage != sampleUsage() {
			t.Fatalf("outcome for %s carried %+v, want the probed usage", outcome.AccountID, outcome.Snapshot.Usage)
		}
		if outcome.Snapshot.FetchedAt == 0 {
			t.Fatalf("outcome for %s has no fetched-at", outcome.AccountID)
		}
	}
	_, ids := env.authorizer.usageSeen()
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	if len(sorted) != 2 || sorted[0] != "account-1" || sorted[1] != "account-2" {
		t.Fatalf("usage probed ids %v, want account-1 and account-2 once each", ids)
	}
}

// Per-account probe failures ride that account's card: the failing
// account keeps its last good timestamp (none, if it never settled),
// the healthy one answers normally, and both are probed — an outage of
// one account must not fail the fleet check for the other.
func TestAFleetQuotaCheckAnswersPerAccountFailures(t *testing.T) {
	env := newTestEnv(t)
	env.signIn(t)
	env.authorizer.setRefreshResult(accountSession("account-2", "second@example.com"), nil)
	env.signIn(t)

	env.authorizer.setUsageScript([]usageAnswer{
		{domain.Usage{}, errors.New("503 service unavailable")},
		{sampleUsage(), nil},
	})
	outcomes := env.service.RefreshQuotaAll(context.Background())
	if len(outcomes) != 2 {
		t.Fatalf("RefreshQuotaAll returned %d outcomes, want 2: %+v", len(outcomes), outcomes)
	}
	failed, good := 0, 0
	for _, outcome := range outcomes {
		if outcome.Snapshot.Err != "" {
			failed++
			if outcome.Snapshot.FetchedAt != 0 {
				t.Fatalf("failed card for %s kept fetched-at %d, want none", outcome.AccountID, outcome.Snapshot.FetchedAt)
			}
			continue
		}
		good++
		if outcome.Snapshot.Usage != sampleUsage() {
			t.Fatalf("good card for %s carried %+v, want the probed usage", outcome.AccountID, outcome.Snapshot.Usage)
		}
	}
	if failed != 1 || good != 1 {
		t.Fatalf("fleet check produced %d failed and %d good cards, want one of each", failed, good)
	}
	if probes := env.authorizer.usageProbeCount(); probes != 2 {
		t.Fatalf("usage probed %d times, want both accounts once", probes)
	}
}

// A fleet check of an empty fleet is an empty list — the shape the
// stdio boundary pins — not an error and not a nil the shell would
// have to special-case.
func TestAFleetQuotaCheckOfAnEmptyFleetAnswersEmpty(t *testing.T) {
	env := newTestEnv(t)

	outcomes := env.service.RefreshQuotaAll(context.Background())
	if len(outcomes) != 0 {
		t.Fatalf("RefreshQuotaAll of an empty fleet returned %d outcomes, want 0", len(outcomes))
	}
}

// Every account is probed, including one that needs sign-in: its card
// reports the refusal the acquire path gives — the answer an owner
// checking the fleet wants, not a silently skipped row.
func TestAFleetQuotaCheckReportsAnAccountThatNeedsSignIn(t *testing.T) {
	env := newTestEnv(t)
	env.store.seed(unrefreshableSession("account-1", "user@example.com"))
	env.restore(t)
	if _, err := env.service.AcquireAccessToken(context.Background(), "account-1"); !errors.Is(err, errNeedsSignIn) {
		t.Fatalf("setup acquire failed with %v, want errNeedsSignIn", err)
	}

	outcomes := env.service.RefreshQuotaAll(context.Background())
	if len(outcomes) != 1 {
		t.Fatalf("RefreshQuotaAll returned %d outcomes, want 1: %+v", len(outcomes), outcomes)
	}
	outcome := outcomes[0]
	if outcome.AccountID != "account-1" {
		t.Fatalf("outcome is for %q, want account-1", outcome.AccountID)
	}
	if outcome.Snapshot.Err != errNotSignedIn.Error() {
		t.Fatalf("reauth card reported %q, want %q", outcome.Snapshot.Err, errNotSignedIn.Error())
	}
	if probes := env.authorizer.usageProbeCount(); probes != 0 {
		t.Fatalf("reauth fleet check probed usage %d times, want 0", probes)
	}
}
