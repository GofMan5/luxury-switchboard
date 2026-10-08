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
	Logout(ctx context.Context) error
	RefreshQuota(ctx context.Context) application.QuotaSnapshot
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
	quotaProbes   int
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

func (service *fakeService) Logout(_ context.Context) error {
	return service.logoutErr
}

// RefreshQuota records the context it was handed — the one assertion the
// harness can make about cancellation riding the command — and answers
// with the canned snapshot, exactly as the real service would.
func (service *fakeService) RefreshQuota(ctx context.Context) application.QuotaSnapshot {
	service.mu.Lock()
	service.quotaProbes++
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

func TestStatusCarriesEveryConnectionField(t *testing.T) {
	service := &fakeService{status: application.Status{
		State:      application.StateSignedIn,
		Email:      "dev@example.com",
		Plan:       "pro",
		AccountID:  "acc_7",
		ProviderID: "codex",
	}}

	payload := payloadOf(t, exchange(t, service, `"method":"codex.status"`)[0])

	want := map[string]any{
		"state":      "signed_in",
		"email":      "dev@example.com",
		"plan":       "pro",
		"accountId":  "acc_7",
		"providerId": "codex",
	}
	if len(payload) != len(want) {
		t.Fatalf("status answered %d fields, want %d: %+v", len(payload), len(want), payload)
	}
	for field, value := range want {
		if payload[field] != value {
			t.Fatalf("status field %s = %#v, want %#v", field, payload[field], value)
		}
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
		status: application.Status{
			State:      application.StateSignedIn,
			Email:      "dev@example.com",
			Plan:       "pro",
			AccountID:  "acc_7",
			ProviderID: "codex",
		},
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

	payload := payloadOf(t, exchange(t, service, `"method":"codex.quota","payload":{}`)[0])

	want := map[string]any{
		"state":      "signed_in",
		"email":      "dev@example.com",
		"plan":       "pro",
		"accountId":  "acc_7",
		"providerId": "codex",
	}
	if _, present := payload["error"]; present {
		t.Fatalf("a settled probe still reported an error: %+v", payload)
	}
	report, ok := payload["quota"].(map[string]any)
	if !ok {
		t.Fatalf("quota command answered without a quota report: %+v", payload)
	}
	if len(payload) != len(want)+1 {
		t.Fatalf("quota answered %d fields, want %d plus the report: %+v", len(payload), len(want), payload)
	}
	for field, value := range want {
		if payload[field] != value {
			t.Fatalf("quota field %s = %#v, want %#v", field, payload[field], value)
		}
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
	// The probe ran once and rode the command's own context, so a shell
	// that aborts the frame also aborts the HTTP call behind it.
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
// while the account fields still tell it why there is nothing to show.
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
}

// TestAQuotaCommandWithPayloadFieldsIsRefused extends the no-fields rule
// to the new command: whatever a client puts in the frame gets the
// standard refusal instead of being silently ignored.
func TestAQuotaCommandWithPayloadFieldsIsRefused(t *testing.T) {
	service := &fakeService{}

	failure := failureOf(t, exchange(t, service, `"method":"codex.quota","payload":{"force":true}`)[0])

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
			State:      application.StateSignedIn,
			Email:      "dev@example.com",
			Plan:       "pro",
			AccountID:  "acc_7",
			ProviderID: "codex",
		},
	}}

	payload := payloadOf(t, exchange(t, service,
		`"method":"codex.import.json","payload":{"text":"  {\"tokens\":{\"id_token\":\"a.b.c\"}}  "}`)[0])

	want := map[string]any{
		"state":      "signed_in",
		"email":      "dev@example.com",
		"plan":       "pro",
		"accountId":  "acc_7",
		"providerId": "codex",
	}
	if len(payload) != len(want) {
		t.Fatalf("json import answered %d fields, want %d: %+v", len(payload), len(want), payload)
	}
	for field, value := range want {
		if payload[field] != value {
			t.Fatalf("json import field %s = %#v, want %#v", field, payload[field], value)
		}
	}
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
			State:      application.StateSignedIn,
			Email:      "dev@example.com",
			ProviderID: "codex",
		},
		ImportedFrom: "auth-codex.json",
	}}

	payload := payloadOf(t, exchange(t, service,
		`"method":"codex.import.files","payload":{"paths":["C:/auth/auth-codex.json","D:/backup/auth.json"]}`)[0])

	if payload["state"] != "signed_in" || payload["email"] != "dev@example.com" {
		t.Fatalf("file import answered the wrong account: %+v", payload)
	}
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
	oversized := strings.Repeat("a", 64*1024+1)
	seventeenPaths := make([]string, 17)
	for index := range seventeenPaths {
		seventeenPaths[index] = "f"
	}
	seventeenPayload, err := json.Marshal(seventeenPaths)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	commands := []string{
		`"method":"codex.import.json","payload":{}`,
		`"method":"codex.import.json","payload":{"text":"   "}`,
		`"method":"codex.import.json","payload":{"text":"` + oversized + `"}`,
		`"method":"codex.import.json","payload":{"text":"ok","note":"x"}`,
		`"method":"codex.import.files","payload":{"paths":[]}`,
		`"method":"codex.import.files","payload":{"paths":` + string(seventeenPayload) + `}`,
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
			State:      application.StateSignedIn,
			Email:      "dev@example.com",
			Plan:       "pro",
			AccountID:  "acc_7",
			ProviderID: "codex",
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
	want := map[string]any{
		"loginPhase": "waiting",
		"loginError": "browser did not answer in time",
		"state":      "signed_in",
		"email":      "dev@example.com",
		"plan":       "pro",
		"accountId":  "acc_7",
		"providerId": "codex",
	}
	if len(payload) != len(want) {
		t.Fatalf("push carried %d fields, want %d: %+v", len(payload), len(want), payload)
	}
	for field, value := range want {
		if payload[field] != value {
			t.Fatalf("push field %s = %#v, want %#v", field, payload[field], value)
		}
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
	for _, field := range []string{"loginPhase", "state", "email", "plan", "accountId", "providerId"} {
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

	// None of the codex commands takes fields, so all three payloads are
	// wrong in different shapes: an unknown field, a non-object, and a
	// typed field on a command that wants none. Each runs through its own
	// one-command exchange: a single outstanding job always fits the
	// bounded queue, while three frames fired together can overflow it
	// and draw "busy" instead — correct server behavior, just not what
	// this test is about.
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
		status: application.Status{State: application.StateSignedIn, ProviderID: "codex"},
	}}

	results := exchange(t, service, `"method":"codex.login.status"`, `"method":"codex.status"`)

	failure := failureOf(t, results[0])
	if failure["code"] != "handler_panicked" {
		t.Fatalf("error code = %#v, want handler_panicked", failure["code"])
	}
	// The worker survives the panic and answers the next command: one
	// broken command must not corrupt the frame stream.
	payload := payloadOf(t, results[1])
	if payload["state"] != "signed_in" || payload["providerId"] != "codex" {
		t.Fatalf("status after a panic carried the wrong values: %+v", payload)
	}
}
