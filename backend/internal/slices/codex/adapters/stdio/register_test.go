package stdio_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	codexstdio "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/adapters/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// codexService mirrors the adapter's consumer interface so the harness can
// accept the fake below (and its panicking variant) without reaching into
// the adapter package for an unexported name.
type codexService interface {
	LoginStart() (string, error)
	LoginStatus() application.LoginStatus
	LoginCancel()
	DeviceLoginStart() (application.DeviceLoginView, error)
	ImportJSON(ctx context.Context, text string) (application.ImportResult, error)
	ImportFiles(ctx context.Context, paths []string) (application.ImportResult, error)
	Status() application.Status
	Logout(ctx context.Context, accountID string, removeEntry bool) error
	RefreshQuota(ctx context.Context, accountID string) application.QuotaSnapshot
	OnChanged(func(application.Snapshot))
}

// fakeService drives the wire without the OAuth machinery behind it: the
// adapter's contract is only what it calls and what it answers, and both
// are visible here. Canned fields are set before the server runs and never
// mutated; only the counters move, always under the mutex, because
// handlers run on server workers while the test reads the counts.
type fakeService struct {
	authorizeURL string
	startErr     error
	login        application.LoginStatus
	status       application.Status
	logoutErr    error

	deviceStartErr  error
	deviceView      application.DeviceLoginView
	importJSONErr   error
	importJSONRes   application.ImportResult
	importFilesErr  error
	importFilesRes  application.ImportResult
	importPathsFail bool
	quotaSnapshot   application.QuotaSnapshot

	mu            sync.Mutex
	cancelCalls   int
	subscriptions int
	listeners     []func(application.Snapshot)
	importTexts   []string
	importPaths   [][]string
	logoutRemoves []bool
	logoutIDs     []string
	quotaProbes   int
	probedIDs     []string
	quotaCtxs     []context.Context
}

func (service *fakeService) LoginStart() (string, error) {
	if service.startErr != nil {
		return "", service.startErr
	}
	return service.authorizeURL, nil
}

func (service *fakeService) LoginStatus() application.LoginStatus {
	return service.login
}

func (service *fakeService) LoginCancel() {
	service.mu.Lock()
	service.cancelCalls++
	service.mu.Unlock()
}

func (service *fakeService) DeviceLoginStart() (application.DeviceLoginView, error) {
	if service.deviceStartErr != nil {
		return application.DeviceLoginView{}, service.deviceStartErr
	}
	return service.deviceView, nil
}

func (service *fakeService) ImportJSON(_ context.Context, text string) (application.ImportResult, error) {
	service.mu.Lock()
	service.importTexts = append(service.importTexts, text)
	service.mu.Unlock()
	if service.importJSONErr != nil {
		return application.ImportResult{}, service.importJSONErr
	}
	return service.importJSONRes, nil
}

func (service *fakeService) ImportFiles(_ context.Context, paths []string) (application.ImportResult, error) {
	service.mu.Lock()
	service.importPaths = append(service.importPaths, paths)
	service.mu.Unlock()
	if service.importPathsFail {
		return application.ImportResult{}, fmt.Errorf("codex import failed: none of the %d files produced a codex session", len(paths))
	}
	if service.importFilesErr != nil {
		return application.ImportResult{}, service.importFilesErr
	}
	return service.importFilesRes, nil
}

func (service *fakeService) Status() application.Status {
	return service.status
}

func (service *fakeService) Logout(_ context.Context, accountID string, removeEntry bool) error {
	service.mu.Lock()
	service.logoutRemoves = append(service.logoutRemoves, removeEntry)
	service.logoutIDs = append(service.logoutIDs, accountID)
	service.mu.Unlock()
	return service.logoutErr
}

// RefreshQuota records the context it was handed — the one assertion the
// harness can make about cancellation riding the command — and answers
// with the canned snapshot, exactly as the real service would.
func (service *fakeService) RefreshQuota(ctx context.Context, accountID string) application.QuotaSnapshot {
	service.mu.Lock()
	service.quotaProbes++
	service.probedIDs = append(service.probedIDs, accountID)
	service.quotaCtxs = append(service.quotaCtxs, ctx)
	service.mu.Unlock()
	return service.quotaSnapshot
}

func (service *fakeService) OnChanged(listener func(application.Snapshot)) {
	service.mu.Lock()
	service.subscriptions++
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}

func (service *fakeService) cancelCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.cancelCalls
}

func (service *fakeService) subscriptionCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.subscriptions
}

// quotaProbeCtxs returns the contexts the quota handler passed down, in
// command order, so a test can assert the probe rode the command's own
// context instead of a background one the shell cannot cancel.
func (service *fakeService) quotaProbeCtxs(t *testing.T) []context.Context {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]context.Context(nil), service.quotaCtxs...)
}

// logoutRemoveFlags returns the remove flags the handlers passed down,
// in command order, so a test can assert the wire payload's delete bit.
func (service *fakeService) logoutRemoveFlags(t *testing.T) []bool {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]bool(nil), service.logoutRemoves...)
}

// logoutAccountIDs returns the account ids the handlers passed down, in
// command order, so a test can assert which session the wire asked for.
func (service *fakeService) logoutAccountIDs(t *testing.T) []string {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]string(nil), service.logoutIDs...)
}

// quotaAccountIDs returns the account ids the quota handler probed, in
// command order, so a test can assert the probe reached the account the
// card named.
func (service *fakeService) quotaAccountIDs(t *testing.T) []string {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]string(nil), service.probedIDs...)
}

// importedTexts returns the texts the handlers passed down, in command
// order, so a test can assert what the parser would have received.
func (service *fakeService) importedTexts(t *testing.T) []string {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]string(nil), service.importTexts...)
}

// importedPathsLists returns each import's path list in command order,
// so a test can assert the paths arrived unchanged and in order.
func (service *fakeService) importedPathsLists(t *testing.T) [][]string {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([][]string(nil), service.importPaths...)
}

// change fires the snapshot listeners the way the application does: with
// no lock held, from whatever goroutine the transition happened on.
func (service *fakeService) change(snapshot application.Snapshot) {
	service.mu.Lock()
	listeners := make([]func(application.Snapshot), len(service.listeners))
	copy(listeners, service.listeners)
	service.mu.Unlock()
	for _, listener := range listeners {
		listener(snapshot)
	}
}

// panickingStatusService breaks one handler the way a real defect would,
// to prove the protocol survives it.
type panickingStatusService struct {
	fakeService
}

func (service *panickingStatusService) LoginStatus() application.LoginStatus {
	panic("login status exploded")
}

// exchange drives commands as real protocol frames through a real server,
// so the assertions see the exact bytes the desktop shell would read. One
// worker keeps the answers in input order; every line of the transcript
// must parse, because a corrupted frame must fail here rather than in an
// assertion that never ran. Two commands is the ceiling: the one-worker
// job queue holds two, and a third in-flight frame races the worker for
// the last slot — the server's "busy" refusal is correct bounded-queue
// behavior, but it is not the answer a caller meant to assert on.
func exchange(t *testing.T, service codexService, commands ...string) []map[string]any {
	t.Helper()
	var input strings.Builder
	for index, command := range commands {
		input.WriteString(`{"v":1,"id":"req` + strconv.Itoa(index) + `","type":"command",` + command + "}\n")
	}
	var output strings.Builder
	server := platform.NewServer(strings.NewReader(input.String()), &output, 1)
	codexstdio.Register(server, service)
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var results []map[string]any
	for _, line := range linesOf(t, output.String()) {
		frame := parseFrame(t, line)
		if frame["type"] == "result" {
			results = append(results, frame)
		}
	}
	if len(results) != len(commands) {
		t.Fatalf("answered %d of %d commands, transcript: %s", len(results), len(commands), output.String())
	}
	return results
}

// linesOf splits a transcript into frame lines, dropping nothing but the
// trailing newline.
func linesOf(t *testing.T, transcript string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(transcript), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// parseFrame reads one frame as a JSON object, keeping numbers exact.
func parseFrame(t *testing.T, line string) map[string]any {
	t.Helper()
	frame := make(map[string]any)
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	if decoder.Decode(&frame) != nil {
		t.Fatalf("unreadable frame: %s", line)
	}
	return frame
}

// payloadOf asserts the protocol's object-result rule: every successful
// command answers with a JSON object, including the deliberately empty
// ones from cancel and logout.
func payloadOf(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	if frame["ok"] != true {
		t.Fatalf("command failed: %+v", frame)
	}
	payload, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("command did not answer with an object: %+v", frame)
	}
	return payload
}

// failureOf asserts the frame failed and returns its error object.
func failureOf(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	if frame["ok"] != false {
		t.Fatalf("command unexpectedly succeeded: %+v", frame)
	}
	failure, ok := frame["error"].(map[string]any)
	if !ok {
		t.Fatalf("command failed without an error object: %+v", frame)
	}
	return failure
}

func TestLoginStartAnswersTheAuthorizeURL(t *testing.T) {
	authorizeURL := "https://auth.openai.com/oauth/authorize?state=st-4f2&code_challenge=b3N1c2NvcGV"
	service := &fakeService{authorizeURL: authorizeURL}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.login.start","payload":{}`)[0])

	if payload["authorizeUrl"] != authorizeURL {
		t.Fatalf("authorizeUrl = %#v, want %q", payload["authorizeUrl"], authorizeURL)
	}
	if len(payload) != 1 {
		t.Fatalf("login start answered more than the URL: %+v", payload)
	}
}

func TestLoginStartFailureCarriesTheServiceError(t *testing.T) {
	service := &fakeService{startErr: errors.New("codex login redirect listener could not bind")}

	failure := failureOf(t, exchange(t, service, `"method":"codex.login.start"`)[0])

	if failure["code"] != "codex_login_failed" {
		t.Fatalf("error code = %#v, want codex_login_failed", failure["code"])
	}
	if failure["message"] != "codex login redirect listener could not bind" {
		t.Fatalf("error message = %#v, want the service's own text", failure["message"])
	}
}

func TestLoginStatusCarriesThePhaseAndItsFailure(t *testing.T) {
	service := &fakeService{login: application.LoginStatus{
		Phase: application.PhaseError,
		Err:   "browser did not answer in time",
	}}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.login.status"`)[0])

	if payload["phase"] != "error" {
		t.Fatalf("phase = %#v, want error", payload["phase"])
	}
	if payload["error"] != "browser did not answer in time" {
		t.Fatalf("error = %#v, want the sign-in dialog's failure text", payload["error"])
	}
}

func TestLoginStatusOmitsTheErrorWhileThereIsNone(t *testing.T) {
	service := &fakeService{login: application.LoginStatus{Phase: application.PhaseWaiting}}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.login.status"`)[0])

	if payload["phase"] != "waiting" {
		t.Fatalf("phase = %#v, want waiting", payload["phase"])
	}
	if _, present := payload["error"]; present {
		t.Fatalf("a phase without a failure still reported one: %+v", payload)
	}
	if len(payload) != 1 {
		t.Fatalf("login status answered more than the phase: %+v", payload)
	}
}

func TestLoginCancelAnswersAnEmptyObjectEvenMidLogin(t *testing.T) {
	service := &fakeService{login: application.LoginStatus{Phase: application.PhaseExchanging}}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.login.cancel"`)[0])

	if len(payload) != 0 {
		t.Fatalf("cancel answered more than an empty object: %+v", payload)
	}
	if count := service.cancelCount(); count != 1 {
		t.Fatalf("cancel reached the service %d times, want 1", count)
	}
}

func TestDeviceLoginStartAnswersTheCodeAndTheVerificationPage(t *testing.T) {
	service := &fakeService{deviceView: application.DeviceLoginView{
		UserCode:            "WLXB-DQK2",
		VerificationURL:     "https://auth.openai.com/codex/device",
		PollIntervalSeconds: 5,
	}}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.login.device.start","payload":{}`)[0])

	want := map[string]any{
		"userCode":            "WLXB-DQK2",
		"verificationUrl":     "https://auth.openai.com/codex/device",
		"pollIntervalSeconds": json.Number("5"),
	}
	if len(payload) != len(want) {
		t.Fatalf("device start answered %d fields, want %d: %+v", len(payload), len(want), payload)
	}
	for field, value := range want {
		if payload[field] != value {
			t.Fatalf("device start field %s = %#v, want %#v", field, payload[field], value)
		}
	}
}

func TestDeviceLoginStartFailureCarriesTheServiceError(t *testing.T) {
	service := &fakeService{deviceStartErr: errors.New("codex login is already in progress")}

	failure := failureOf(t, exchange(t, service, `"method":"codex.login.device.start"`)[0])

	if failure["code"] != "codex_login_failed" {
		t.Fatalf("error code = %#v, want codex_login_failed", failure["code"])
	}
	if failure["message"] != "codex login is already in progress" {
		t.Fatalf("error message = %#v, want the service's own text", failure["message"])
	}
}

func TestDeviceLoginStartTakesNoPayloadFields(t *testing.T) {
	service := &fakeService{}

	failure := failureOf(t, exchange(t, service,
		`"method":"codex.login.device.start","payload":{"userCode":"FAKE"}`)[0])

	if failure["code"] != "invalid_payload" {
		t.Fatalf("error code = %#v, want invalid_payload", failure["code"])
	}
}

func TestLoginStatusCarriesTheDeviceCodeWhileOneIsShown(t *testing.T) {
	service := &fakeService{login: application.LoginStatus{
		Phase:                 application.PhaseWaiting,
		DeviceUserCode:        "WLXB-DQK2",
		DeviceVerificationURL: "https://auth.openai.com/codex/device",
	}}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.login.status"`)[0])

	if payload["deviceUserCode"] != "WLXB-DQK2" {
		t.Fatalf("deviceUserCode = %#v, want the code the endpoint issued", payload["deviceUserCode"])
	}
	if payload["deviceVerificationUrl"] != "https://auth.openai.com/codex/device" {
		t.Fatalf("deviceVerificationUrl = %#v, want the verification page", payload["deviceVerificationUrl"])
	}
}

// assertAccountRow pins one status row's exact shape: the id-token
// labels plus the account's own state and the Providers-page entry it
// serves — no more, because a row is not a second copy of the status.
func assertAccountRow(t *testing.T, row any, accountID, email, plan, state, providerID string) {
	t.Helper()
	fields, ok := row.(map[string]any)
	if !ok {
		t.Fatalf("account row is not an object: %+v", row)
	}
	want := map[string]any{
		"accountId":  accountID,
		"email":      email,
		"plan":       plan,
		"state":      state,
		"providerId": providerID,
	}
	if len(fields) != len(want) {
		t.Fatalf("account row answered %d fields, want %d: %+v", len(fields), len(want), fields)
	}
	for field, value := range want {
		if fields[field] != value {
			t.Fatalf("account row field %s = %#v, want %#v", field, fields[field], value)
		}
	}
}

func TestStatusCarriesTheStateAndEveryAccountRow(t *testing.T) {
	service := &fakeService{status: application.Status{
		State: application.StateSignedIn,
		Accounts: []application.AccountStatus{
			{
				AccountID:  "acc_7",
				Email:      "dev@example.com",
				Plan:       "pro",
				State:      application.StateSignedIn,
				ProviderID: "codex",
			},
			{
				AccountID: "acc_9",
				Email:     "ops@example.com",
				Plan:      "plus",
				State:     application.StateReauthNeeded,
			},
		},
	}}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.status"`)[0])

	if payload["state"] != "signed_in" {
		t.Fatalf("status state = %#v, want the aggregate signed_in", payload["state"])
	}
	rows, ok := payload["accounts"].([]any)
	if !ok {
		t.Fatalf("status answered without an accounts array: %+v", payload)
	}
	if len(rows) != 2 {
		t.Fatalf("status answered %d account rows, want 2: %+v", len(rows), payload)
	}
	assertAccountRow(t, rows[0], "acc_7", "dev@example.com", "pro", "signed_in", "codex")
	// A second row proves the array is real, not a re-labelled single
	// account: it keeps its own state — reauth — even while the aggregate
	// says signed in, and an entry that has not been provisioned yet
	// carries an empty provider id rather than a borrowed one.
	assertAccountRow(t, rows[1], "acc_9", "ops@example.com", "plus", "reauth_needed", "")
	if len(payload) != 2 {
		t.Fatalf("status answered %d fields, want state and accounts: %+v", len(payload), payload)
	}
}

// TestStatusBeforeAnyAccountAnswersAnEmptyArray pins the JSON shape: a
// status with no accounts yet carries an empty array, not null — the
// pane must tell "no accounts" apart from a field that went missing.
func TestStatusBeforeAnyAccountAnswersAnEmptyArray(t *testing.T) {
	service := &fakeService{}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.status"`)[0])

	rows, ok := payload["accounts"].([]any)
	if !ok {
		t.Fatalf("accounts before any sign-in = %#v, want an empty array", payload["accounts"])
	}
	if len(rows) != 0 {
		t.Fatalf("accounts before any sign-in = %v, want empty", rows)
	}
}

// quotaWindowOf fetches one meter out of the quota report and keeps the
// fatality at the field the test named.
func quotaWindowOf(t *testing.T, report map[string]any, field string) map[string]any {
	t.Helper()
	window, ok := report[field].(map[string]any)
	if !ok {
		t.Fatalf("quota report field %s is not an object: %+v", field, report)
	}
	return window
}

// assertQuotaWindow pins one meter's exact shape: the fields the endpoint
// reported, and no invented ones for the fields it did not.
func assertQuotaWindow(t *testing.T, window map[string]any, present bool, remaining json.Number, extra map[string]json.Number) {
	t.Helper()
	if window["present"] != present {
		t.Fatalf("window present = %#v, want %v", window["present"], present)
	}
	if window["remainingPercent"] != remaining {
		t.Fatalf("window remainingPercent = %#v, want %s", window["remainingPercent"], remaining.String())
	}
	if len(window) != 2+len(extra) {
		t.Fatalf("window answered %d fields, want %d: %+v", len(window), 2+len(extra), window)
	}
	for field, value := range extra {
		if window[field] != value {
			t.Fatalf("window field %s = %#v, want %s", field, window[field], value.String())
		}
	}
}

func TestQuotaAnswersTheAccountAndBothMeters(t *testing.T) {
	service := &fakeService{
		quotaSnapshot: application.QuotaSnapshot{
			FetchedAt: 1_800_000_000,
			Usage: domain.Usage{
				PlanType: "plus",
				Primary: domain.QuotaWindow{
					Present:          true,
					RemainingPercent: 38,
					WindowMinutes:    5,
					ResetAt:          time.Unix(1_800_001_800, 0).UTC(),
				},
				Secondary: domain.QuotaWindow{
					Present:          true,
					RemainingPercent: 95,
					WindowMinutes:    10_080,
					ResetAt:          time.Unix(1_800_086_000, 0).UTC(),
				},
			},
		},
	}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.quota","payload":{"accountId":"acc_7"}`)[0])

	// The card's labels travel in the status rows; this answer carries
	// only what the probe itself learned: the account and its meters.
	if payload["accountId"] != "acc_7" {
		t.Fatalf("quota accountId = %#v, want the account the frame named", payload["accountId"])
	}
	if _, present := payload["error"]; present {
		t.Fatalf("a settled probe still reported an error: %+v", payload)
	}
	report, ok := payload["quota"].(map[string]any)
	if !ok {
		t.Fatalf("quota command answered without a quota report: %+v", payload)
	}
	if len(payload) != 2 {
		t.Fatalf("quota answered %d fields, want the account and the report: %+v", len(payload), payload)
	}
	if report["fetchedAt"] != json.Number("1800000000") {
		t.Fatalf("quota fetchedAt = %#v, want the probe's epoch", report["fetchedAt"])
	}
	if report["planType"] != "plus" {
		t.Fatalf("quota planType = %#v, want plus", report["planType"])
	}
	assertQuotaWindow(t, quotaWindowOf(t, report, "primary"), true, json.Number("38"), map[string]json.Number{
		"windowMinutes": json.Number("5"),
		"resetAt":       json.Number("1800001800"),
	})
	assertQuotaWindow(t, quotaWindowOf(t, report, "secondary"), true, json.Number("95"), map[string]json.Number{
		"windowMinutes": json.Number("10080"),
		"resetAt":       json.Number("1800086000"),
	})
	// The probe ran once, reached the account the frame named, and rode
	// the command's own context, so a shell that aborts the frame also
	// aborts the HTTP call behind it.
	probes := service.quotaAccountIDs(t)
	if len(probes) != 1 || probes[0] != "acc_7" {
		t.Fatalf("quota command probed accounts %v, want only acc_7", probes)
	}
	ctxs := service.quotaProbeCtxs(t)
	if len(ctxs) != 1 {
		t.Fatalf("quota command probed %d times, want 1", len(ctxs))
	}
	if ctxs[0] == context.Background() {
		t.Fatal("quota probe ran on context.Background, want the command's cancellable context")
	}
}

// TestQuotaOmitsTheMeterTheEndpointDidNotReport pins absence over
// invention: a window the endpoint never named is a meter without width
// or deadline, never a zero-minute window that resets at the epoch.
func TestQuotaOmitsTheMeterTheEndpointDidNotReport(t *testing.T) {
	service := &fakeService{
		quotaSnapshot: application.QuotaSnapshot{
			FetchedAt: 1_800_000_000,
			Usage: domain.Usage{
				Primary:   domain.QuotaWindow{Present: true, RemainingPercent: 40},
				Secondary: domain.QuotaWindow{Present: false, RemainingPercent: 100},
			},
		},
	}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.quota"`)[0])

	report, ok := payload["quota"].(map[string]any)
	if !ok {
		t.Fatalf("quota command answered without a quota report: %+v", payload)
	}
	if _, present := report["planType"]; present {
		t.Fatalf("quota report invented a planType: %+v", report)
	}
	assertQuotaWindow(t, quotaWindowOf(t, report, "primary"), true, json.Number("40"), nil)
	assertQuotaWindow(t, quotaWindowOf(t, report, "secondary"), false, json.Number("100"), nil)
}

// TestAFailedQuotaProbeKeepsTheLastGoodWindows pins the failure shape:
// the probe's verdict travels as a result field the card can act on,
// while the quota block still carries the last settled numbers — the
// failed command is an answer, not a refusal.
func TestAFailedQuotaProbeKeepsTheLastGoodWindows(t *testing.T) {
	service := &fakeService{
		quotaSnapshot: application.QuotaSnapshot{
			FetchedAt: 1_800_000_000,
			Usage: domain.Usage{
				Primary: domain.QuotaWindow{Present: true, RemainingPercent: 12, WindowMinutes: 5},
			},
			Err: "codex oauth usage probe failed: http 503",
		},
	}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.quota"`)[0])

	if payload["error"] != "codex oauth usage probe failed: http 503" {
		t.Fatalf("quota error = %#v, want the probe's own text", payload["error"])
	}
	report, ok := payload["quota"].(map[string]any)
	if !ok {
		t.Fatalf("a failed probe dropped the last good windows: %+v", payload)
	}
	assertQuotaWindow(t, quotaWindowOf(t, report, "primary"), true, json.Number("12"), map[string]json.Number{
		"windowMinutes": json.Number("5"),
	})
}

// TestAQuotaCommandBeforeAnyProbeSettled pins the empty state: no quota
// block at all — the card shows "nothing known yet", not empty meters —
// while the echoed account id keeps the answer addressable even when a
// shell that predates accounts sends an empty payload.
func TestAQuotaCommandBeforeAnyProbeSettled(t *testing.T) {
	service := &fakeService{
		quotaSnapshot: application.QuotaSnapshot{Err: "codex is not signed in"},
	}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.quota"`)[0])

	if _, present := payload["quota"]; present {
		t.Fatalf("quota command invented a report before any probe settled: %+v", payload)
	}
	if payload["error"] != "codex is not signed in" {
		t.Fatalf("quota error = %#v, want the not-signed-in verdict", payload["error"])
	}
	if payload["accountId"] != "" {
		t.Fatalf("quota accountId = %#v, want the empty account of a fieldless frame", payload["accountId"])
	}
}

// TestAQuotaCommandWithPayloadFieldsIsRefused keeps the quota payload a
// closed contract: the account it takes is the only field, so anything
// else a client puts in the frame gets the standard refusal instead of
// being silently ignored.
func TestAQuotaCommandWithPayloadFieldsIsRefused(t *testing.T) {
	service := &fakeService{}

	failure := failureOf(t, exchange(t, service, `"method":"codex.quota","payload":{"accountId":"acc_7","force":true}`)[0])

	if failure["code"] != "invalid_payload" {
		t.Fatalf("error code = %#v, want invalid_payload", failure["code"])
	}
	if failure["message"] != "codex command payload is invalid" {
		t.Fatalf("error message = %#v, want the adapter's refusal text", failure["message"])
	}
	if ctxs := service.quotaProbeCtxs(t); len(ctxs) != 0 {
		t.Fatalf("refused payload still probed %d times, want 0", len(ctxs))
	}
}

func TestLogoutAnswersAnEmptyObject(t *testing.T) {
	service := &fakeService{}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.logout"`)[0])

	if len(payload) != 0 {
		t.Fatalf("logout answered more than an empty object: %+v", payload)
	}
	// An empty payload is the plain disconnect the command has always
	// been: no account named means every account, and the flag must
	// default to false, not to delete.
	if removes := service.logoutRemoveFlags(t); len(removes) != 1 || removes[0] {
		t.Fatalf("logout remove flags = %v, want a single false", removes)
	}
	if ids := service.logoutAccountIDs(t); len(ids) != 1 || ids[0] != "" {
		t.Fatalf("logout account ids = %v, want a single empty id", ids)
	}
}

// The remove flag is the delete path on the wire: an explicit
// {"remove": true} reaches the service, the account id rides the same
// frame so the delete names its session, and an unknown field next to
// them is still refused — the payload stays a closed contract.
func TestLogoutRemovePassesTheFlagThrough(t *testing.T) {
	service := &fakeService{}

	exchange(t, service, `"method":"codex.logout","payload":{"remove":true,"accountId":"acc_7"}`)

	removes := service.logoutRemoveFlags(t)
	if len(removes) != 1 || !removes[0] {
		t.Fatalf("logout remove flags = %v, want a single true", removes)
	}
	ids := service.logoutAccountIDs(t)
	if len(ids) != 1 || ids[0] != "acc_7" {
		t.Fatalf("logout account ids = %v, want a single acc_7", ids)
	}

	unrecognised := &fakeService{}
	failureOf(t, exchange(t, unrecognised, `"method":"codex.logout","payload":{"remove":true,"purge":true}`)[0])
	if removes := unrecognised.logoutRemoveFlags(t); len(removes) != 0 {
		t.Fatalf("a refused payload still reached the service: %v", removes)
	}
}

func TestLogoutFailureCarriesTheServiceError(t *testing.T) {
	service := &fakeService{logoutErr: errors.New("codex session could not be cleared: store unavailable")}

	failure := failureOf(t, exchange(t, service, `"method":"codex.logout"`)[0])

	if failure["code"] != "codex_logout_failed" {
		t.Fatalf("error code = %#v, want codex_logout_failed", failure["code"])
	}
	if failure["message"] != "codex session could not be cleared: store unavailable" {
		t.Fatalf("error message = %#v, want the service's own text", failure["message"])
	}
}

func TestImportJSONSendsTrimmedTextAndAnswersTheLandedAccount(t *testing.T) {
	service := &fakeService{importJSONRes: application.ImportResult{
		Status: application.Status{
			State: application.StateSignedIn,
			Accounts: []application.AccountStatus{{
				AccountID:  "acc_7",
				Email:      "dev@example.com",
				Plan:       "pro",
				State:      application.StateSignedIn,
				ProviderID: "codex",
			}},
		},
	}}

	payload := payloadOf(t, exchange(t, service,
		`"method":"codex.import.json","payload":{"text":"  {\"tokens\":{\"id_token\":\"a.b.c\"}}  "}`)[0])

	if payload["state"] != "signed_in" {
		t.Fatalf("json import state = %#v, want signed_in", payload["state"])
	}
	rows, ok := payload["accounts"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("json import answered %v account rows, want the landed account: %+v", payload["accounts"], payload)
	}
	assertAccountRow(t, rows[0], "acc_7", "dev@example.com", "pro", "signed_in", "codex")
	// A paste drags whitespace with it; the wire owes the parser the
	// trimmed text because leading spaces would disguise the JSON it
	// keys on. Pasted JSON has no file to name, so importedFrom must
	// stay absent, not present-and-empty.
	texts := service.importedTexts(t)
	if len(texts) != 1 {
		t.Fatalf("import reached the service %d times, want 1", len(texts))
	}
	if texts[0] != `{"tokens":{"id_token":"a.b.c"}}` {
		t.Fatalf("service received %#v, want the trimmed paste", texts[0])
	}
	if _, present := payload["importedFrom"]; present {
		t.Fatalf("json import named a file it never read: %+v", payload)
	}
}

func TestImportFilesSendsEveryPathInOrderAndAnswersTheFileItCameFrom(t *testing.T) {
	service := &fakeService{importFilesRes: application.ImportResult{
		Status: application.Status{
			State: application.StateSignedIn,
			Accounts: []application.AccountStatus{{
				AccountID:  "acc_7",
				Email:      "dev@example.com",
				State:      application.StateSignedIn,
				ProviderID: "codex",
			}},
		},
		ImportedFrom: "auth-codex.json",
	}}

	payload := payloadOf(t, exchange(t, service,
		`"method":"codex.import.files","payload":{"paths":["C:/auth/auth-codex.json","D:/backup/auth.json"]}`)[0])

	if payload["state"] != "signed_in" {
		t.Fatalf("file import answered the wrong state: %+v", payload)
	}
	rows, ok := payload["accounts"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("file import answered %v account rows, want the landed account: %+v", payload["accounts"], payload)
	}
	assertAccountRow(t, rows[0], "acc_7", "dev@example.com", "", "signed_in", "codex")
	if payload["importedFrom"] != "auth-codex.json" {
		t.Fatalf("importedFrom = %#v, want the base name of the winning file", payload["importedFrom"])
	}
	pathLists := service.importedPathsLists(t)
	if len(pathLists) != 1 || len(pathLists[0]) != 2 {
		t.Fatalf("import reached the service with paths %v, want the two paths sent", pathLists)
	}
	if pathLists[0][0] != "C:/auth/auth-codex.json" || pathLists[0][1] != "D:/backup/auth.json" {
		t.Fatalf("service received paths %v, want them unchanged and in order", pathLists[0])
	}
}

// Fleet-sized imports fit in one call. A paste at the text cap and a
// file list at the path cap both reach the service unchanged — the
// caps refuse what is past them, never a fleet the caps claim to
// admit. This is the boundary the caps exist to draw, so it is pinned
// from the passing side, not only from the refusal side.
func TestFleetSizedImportsReachTheServiceInOneCall(t *testing.T) {
	service := &fakeService{importJSONRes: application.ImportResult{
		Status: application.Status{State: application.StateSignedIn},
	}, importFilesRes: application.ImportResult{
		Status: application.Status{State: application.StateSignedIn},
	}}

	fleetPaste := strings.Repeat("a", 192*1024)
	pastePayload := payloadOf(t, exchange(t, service,
		`"method":"codex.import.json","payload":{"text":"`+fleetPaste+`"}`)[0])
	if pastePayload["state"] != "signed_in" {
		t.Fatalf("fleet paste import answered the wrong state: %+v", pastePayload)
	}
	if texts := service.importedTexts(t); len(texts) != 1 || texts[0] != fleetPaste {
		t.Fatalf("paste reached the service as %d texts, want one unchanged fleet paste", len(texts))
	}

	fleetPaths := make([]string, 128)
	for index := range fleetPaths {
		fleetPaths[index] = fmt.Sprintf("C:/fleet/auth-%d.json", index)
	}
	fleetPayload, err := json.Marshal(fleetPaths)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	filesPayload := payloadOf(t, exchange(t, service,
		`"method":"codex.import.files","payload":{"paths":`+string(fleetPayload)+`}`)[0])
	if filesPayload["state"] != "signed_in" {
		t.Fatalf("fleet files import answered the wrong state: %+v", filesPayload)
	}
	if lists := service.importedPathsLists(t); len(lists) != 1 || len(lists[0]) != 128 {
		t.Fatalf("files reached the service as %d lists, want one list of 128 paths", len(lists))
	}
}

func TestImportFailureCarriesTheServiceError(t *testing.T) {
	service := &fakeService{importJSONErr: errors.New("no codex credentials found")}

	failure := failureOf(t, exchange(t, service,
		`"method":"codex.import.json","payload":{"text":"OPENAI_API_KEY=not-a-token"}`)[0])

	if failure["code"] != "codex_import_failed" {
		t.Fatalf("error code = %#v, want codex_import_failed", failure["code"])
	}
	if failure["message"] != "no codex credentials found" {
		t.Fatalf("error message = %#v, want the service's own text", failure["message"])
	}
}

func TestImportFilesFailureCarriesTheAllFailedSummary(t *testing.T) {
	service := &fakeService{importPathsFail: true}

	failure := failureOf(t, exchange(t, service,
		`"method":"codex.import.files","payload":{"paths":["C:/auth/a.json","C:/auth/b.json"]}`)[0])

	if failure["code"] != "codex_import_failed" {
		t.Fatalf("error code = %#v, want codex_import_failed", failure["code"])
	}
	if failure["message"] != "codex import failed: none of the 2 files produced a codex session" {
		t.Fatalf("error message = %#v, want the all-failed summary", failure["message"])
	}
}

func TestImportPayloadsThatCannotBeSentAreRefused(t *testing.T) {
	service := &fakeService{}

	// Every payload here is shaped like an import but cannot be one:
	// absent, blank and oversized text; and for files an empty list, a
	// list past the cap, a blank entry, and a field the command does
	// not take. All must land in the import's own refusal, not the
	// generic invalid_payload — the dialog has one copy for the flow.
	// Each command runs through its own server: the harness drives one
	// worker, so a batch would answer "busy" before it answered the
	// refusal under test.
	oversized := strings.Repeat("a", 192*1024+1)
	tooManyPaths := make([]string, 129)
	for index := range tooManyPaths {
		tooManyPaths[index] = "f"
	}
	tooManyPayload, err := json.Marshal(tooManyPaths)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	commands := []string{
		`"method":"codex.import.json","payload":{}`,
		`"method":"codex.import.json","payload":{"text":"   "}`,
		`"method":"codex.import.json","payload":{"text":"` + oversized + `"}`,
		`"method":"codex.import.json","payload":{"text":"ok","note":"x"}`,
		`"method":"codex.import.files","payload":{"paths":[]}`,
		`"method":"codex.import.files","payload":{"paths":` + string(tooManyPayload) + `}`,
		`"method":"codex.import.files","payload":{"paths":["C:/auth/a.json",""]}`,
		`"method":"codex.import.files","payload":{"text":"no paths here"}`,
	}

	for _, command := range commands {
		failure := failureOf(t, exchange(t, service, command)[0])
		if failure["code"] != "codex_import_failed" {
			t.Fatalf("error code = %#v, want codex_import_failed", failure["code"])
		}
		if failure["message"] != "codex import payload is invalid" {
			t.Fatalf("error message = %#v, want the import refusal text", failure["message"])
		}
	}
	if calls := service.importedTexts(t); len(calls) != 0 {
		t.Fatalf("refused imports still reached the service %d times", len(calls))
	}
	if calls := service.importedPathsLists(t); len(calls) != 0 {
		t.Fatalf("refused imports still reached the service %d times", len(calls))
	}
}

func TestASnapshotChangeEmitsExactlyOneCodexChangedEvent(t *testing.T) {
	service := &fakeService{}
	var output strings.Builder
	server := platform.NewServer(strings.NewReader(""), &output, 1)
	codexstdio.Register(server, service)

	service.change(application.Snapshot{
		Login: application.LoginStatus{
			Phase: application.PhaseWaiting,
			Err:   "browser did not answer in time",
		},
		Conn: application.Status{
			State: application.StateSignedIn,
			Accounts: []application.AccountStatus{{
				AccountID:  "acc_7",
				Email:      "dev@example.com",
				Plan:       "pro",
				State:      application.StateSignedIn,
				ProviderID: "codex",
			}},
		},
	})

	lines := linesOf(t, output.String())
	if len(lines) != 1 {
		t.Fatalf("a snapshot change emitted %d frames, want exactly 1: %s", len(lines), output.String())
	}
	frame := parseFrame(t, lines[0])
	if frame["type"] != "event" || frame["topic"] != "codex.changed" {
		t.Fatalf("frame is not a codex.changed event: %s", lines[0])
	}
	payload, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("push did not carry an object payload: %s", lines[0])
	}
	if payload["loginPhase"] != "waiting" {
		t.Fatalf("push loginPhase = %#v, want waiting", payload["loginPhase"])
	}
	if payload["loginError"] != "browser did not answer in time" {
		t.Fatalf("push loginError = %#v, want the failure text", payload["loginError"])
	}
	if payload["state"] != "signed_in" {
		t.Fatalf("push state = %#v, want signed_in", payload["state"])
	}
	rows, ok := payload["accounts"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("push carried %v account rows, want the signed-in account: %+v", payload["accounts"], payload)
	}
	assertAccountRow(t, rows[0], "acc_7", "dev@example.com", "pro", "signed_in", "codex")
	if len(payload) != 4 {
		t.Fatalf("push carried %d fields, want the login pair, state and accounts: %+v", len(payload), payload)
	}
}

func TestTheChangedPushOmitsTheLoginErrorWhileItIsEmpty(t *testing.T) {
	service := &fakeService{}
	var output strings.Builder
	server := platform.NewServer(strings.NewReader(""), &output, 1)
	codexstdio.Register(server, service)

	service.change(application.Snapshot{
		Login: application.LoginStatus{Phase: application.PhaseSuccess},
		Conn:  application.Status{State: application.StateSignedOut},
	})

	lines := linesOf(t, output.String())
	if len(lines) != 1 {
		t.Fatalf("a snapshot change emitted %d frames, want exactly 1: %s", len(lines), output.String())
	}
	payload, ok := parseFrame(t, lines[0])["payload"].(map[string]any)
	if !ok {
		t.Fatalf("push did not carry an object payload: %s", lines[0])
	}
	if _, present := payload["loginError"]; present {
		t.Fatalf("push reported an empty login error: %+v", payload)
	}
	for _, field := range []string{"loginPhase", "state", "accounts"} {
		if _, present := payload[field]; !present {
			t.Fatalf("push lost the %s field: %+v", field, payload)
		}
	}
	if payload["loginPhase"] != "success" || payload["state"] != "signed_out" {
		t.Fatalf("push flattened the wrong values: %+v", payload)
	}
}

func TestRegisterSubscribesTheChangeFeedExactlyOnce(t *testing.T) {
	service := &fakeService{}
	server := platform.NewServer(strings.NewReader(""), io.Discard, 1)

	codexstdio.Register(server, service)

	if count := service.subscriptionCount(); count != 1 {
		t.Fatalf("one Register call subscribed the change feed %d times, want exactly 1", count)
	}
}

func TestASecondRegisterIsRefusedBeforeItCanDoubleSubscribe(t *testing.T) {
	service := &fakeService{}
	server := platform.NewServer(strings.NewReader(""), io.Discard, 1)
	codexstdio.Register(server, service)

	defer func() {
		problem := recover()
		if problem == nil {
			t.Fatal("a second Register on the same server was silently accepted")
		}
		if !strings.Contains(fmt.Sprint(problem), "duplicate handler") {
			t.Fatalf("second Register failed for the wrong reason: %v", problem)
		}
		if count := service.subscriptionCount(); count != 1 {
			t.Fatalf("refused Register still subscribed the change feed %d times, want 1", count)
		}
	}()
	codexstdio.Register(server, service)
}

func TestUnexpectedPayloadFieldsAreRefusedNotIgnored(t *testing.T) {
	service := &fakeService{login: application.LoginStatus{Phase: application.PhaseWaiting}}

	// The commands that take no fields stay that way, so all three
	// payloads are wrong in different shapes: an unknown field, a
	// non-object, and a typed field on a command that wants none. Each
	// runs through its own one-command exchange: a single outstanding
	// job always fits the bounded queue, while three frames fired
	// together can overflow it and draw "busy" instead — correct server
	// behavior, just not what this test is about.
	commands := []string{
		`"method":"codex.login.status","payload":{"phase":"waiting"}`,
		`"method":"codex.status","payload":[1,2]`,
		`"method":"codex.login.start","payload":{"provider":"other"}`,
	}
	for _, command := range commands {
		failure := failureOf(t, exchange(t, service, command)[0])
		if failure["code"] != "invalid_payload" {
			t.Fatalf("%s: error code = %#v, want invalid_payload", command, failure["code"])
		}
		if failure["message"] != "codex command payload is invalid" {
			t.Fatalf("%s: error message = %#v, want the adapter's refusal text", command, failure["message"])
		}
	}
}

func TestAPanickingHandlerFailsTheCommandNotTheProtocol(t *testing.T) {
	service := &panickingStatusService{fakeService: fakeService{
		status: application.Status{
			State: application.StateSignedIn,
			Accounts: []application.AccountStatus{{
				AccountID:  "acc_7",
				State:      application.StateSignedIn,
				ProviderID: "codex",
			}},
		},
	}}

	results := exchange(t, service, `"method":"codex.login.status"`, `"method":"codex.status"`)

	failure := failureOf(t, results[0])
	if failure["code"] != "handler_panicked" {
		t.Fatalf("error code = %#v, want handler_panicked", failure["code"])
	}
	// The worker survives the panic and answers the next command: one
	// broken command must not corrupt the frame stream.
	payload := payloadOf(t, results[1])
	rows, ok := payload["accounts"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("status after a panic carried %v account rows, want 1: %+v", payload["accounts"], payload)
	}
	assertAccountRow(t, rows[0], "acc_7", "", "", "signed_in", "codex")
	if payload["state"] != "signed_in" {
		t.Fatalf("status after a panic carried the wrong state: %+v", payload)
	}
}
