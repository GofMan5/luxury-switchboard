// Package stdio exposes the codex preset's application service over the
// versioned stdio protocol: browser and device-code sign-in, credential
// imports, connection status, sign-out and the codex.changed push that
// lets surfaces refetch instead of polling. The adapter adds no logic of
// its own — every value comes from the application layer, which already
// guarantees that login phases and account labels may travel on the wire
// while session tokens never do.
package stdio

import (
	"context"
	"encoding/json"
	"strings"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
)

// codexService is the narrow slice of the application service this adapter
// consumes. The real *application.Service satisfies it; declaring the
// dependency as methods keeps the protocol wiring from reaching past the
// sign-in scenario it serves, and lets the tests drive the wire with a
// fake instead of the OAuth machinery behind it.
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

// LoginStartResult carries the authorize URL the desktop shell opens in
// the system browser. The URL is the public OAuth entry point carrying
// PKCE state, not a credential: it is safe to hand to the browser.
type LoginStartResult struct {
	AuthorizeURL string `json:"authorizeUrl"`
}

// LoginStatusResult is the login flow as the sign-in dialog renders it.
// Error is omitted entirely while empty — a phase without a failure must
// not read as a phase with an empty one — and the device fields appear
// only while a device-code login is showing its code, so the browser
// flow's statuses stay byte-identical to what they were before the
// device flow existed.
type LoginStatusResult struct {
	Phase                 string `json:"phase"`
	Error                 string `json:"error,omitempty"`
	DeviceUserCode        string `json:"deviceUserCode,omitempty"`
	DeviceVerificationURL string `json:"deviceVerificationUrl,omitempty"`
}

// DeviceLoginStartResult is the code the user types into the page they
// open themselves, plus the address of that page. The code is not a
// credential: it is only usable together with the account that approves
// it. PollIntervalSeconds is the cadence the endpoint asked for, clamped
// by the application layer to a whole number of seconds.
type DeviceLoginStartResult struct {
	UserCode            string `json:"userCode"`
	VerificationURL     string `json:"verificationUrl"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
}

// StatusResult is the connection as the accounts pane renders it. State
// is the aggregate the service computes; Accounts carries one row per
// stored account, signed in or not, in the service's stable order. The
// rows are account labels from the id tokens — not secrets — and the
// access tokens the sessions carry never cross this boundary.
type StatusResult struct {
	State    string          `json:"state"`
	Accounts []AccountResult `json:"accounts"`
}

// AccountResult is one codex account as the accounts pane renders it:
// that account's id-token labels plus its own connection state.
// ProviderID names the Providers-page entry the account serves, so a
// pane row and a provider row can be told apart even when the entry
// exists while the account sits signed out.
type AccountResult struct {
	AccountID  string `json:"accountId"`
	Email      string `json:"email"`
	Plan       string `json:"plan"`
	State      string `json:"state"`
	ProviderID string `json:"providerId"`
}

// ImportResult reports an import that landed: the live account status —
// the same state and rows StatusResult carries — plus where the
// session came from, a file base name. ImportedFrom is omitted while
// empty so a pasted-JSON import reads as JSON, not as a file with no
// name.
type ImportResult struct {
	State        string          `json:"state"`
	Accounts     []AccountResult `json:"accounts"`
	ImportedFrom string          `json:"importedFrom,omitempty"`
}

// QuotaResult is one account's quota card on the wire: which account
// was probed plus the last settled usage probe for it. The card's
// labels — email, plan, the row itself — come from the status rows the
// account already travels in; this answer carries only what a probe
// itself learned, so it stays one card, not a second copy of the
// account list. Quota is omitted until some probe has settled, so a
// first failure reads as "nothing known yet" rather than as empty
// meters; Error is omitted while empty for the same reason — a probe's
// outcome is data for the card, not a protocol error, because a failed
// probe deliberately keeps the last good windows.
type QuotaResult struct {
	AccountID string       `json:"accountId"`
	Quota     *QuotaReport `json:"quota,omitempty"`
	Error     string       `json:"error,omitempty"`
}

// QuotaReport is one settled usage probe: when it landed and what it
// said. PlanType is the plan the usage endpoint itself named, omitted
// while empty — the account's id-token plan stays the primary label.
type QuotaReport struct {
	FetchedAt int64             `json:"fetchedAt"`
	PlanType  string            `json:"planType,omitempty"`
	Primary   QuotaWindowResult `json:"primary"`
	Secondary QuotaWindowResult `json:"secondary"`
}

// QuotaWindowResult is one meter's numbers on the wire. WindowMinutes
// and ResetAt are omitted when the endpoint did not report them —
// absence reads as "unknown", not as a zero — and ResetAt is epoch
// seconds, the same unit the endpoint speaks.
type QuotaWindowResult struct {
	Present          bool  `json:"present"`
	RemainingPercent int   `json:"remainingPercent"`
	WindowMinutes    int   `json:"windowMinutes,omitempty"`
	ResetAt          int64 `json:"resetAt,omitempty"`
}

// Register wires the codex commands onto the protocol server and
// subscribes to the service's change feed exactly once: one Register call
// is one codex.changed subscription, and a second call on the same server
// is refused by the platform's duplicate-handler guard rather than
// quietly doubling the feed.
func Register(server *platform.Server, service codexService) {
	server.Handle("codex.login.start", func(_ context.Context, payload json.RawMessage) (any, error) {
		if err := decodeCommand(payload); err != nil {
			return nil, err
		}
		authorizeURL, err := service.LoginStart()
		if err != nil {
			// The application returns login failures as-is by contract —
			// the port conflict text belongs to the listener adapter that
			// produced it, and it carries no token material.
			return nil, platform.MethodError{Code: "codex_login_failed", Message: err.Error()}
		}
		return LoginStartResult{AuthorizeURL: authorizeURL}, nil
	})
	server.Handle("codex.login.status", func(_ context.Context, payload json.RawMessage) (any, error) {
		if err := decodeCommand(payload); err != nil {
			return nil, err
		}
		status := service.LoginStatus()
		return LoginStatusResult{
			Phase:                 string(status.Phase),
			Error:                 status.Err,
			DeviceUserCode:        status.DeviceUserCode,
			DeviceVerificationURL: status.DeviceVerificationURL,
		}, nil
	})
	server.Handle("codex.login.cancel", func(_ context.Context, payload json.RawMessage) (any, error) {
		if err := decodeCommand(payload); err != nil {
			return nil, err
		}
		// Cancel never answers a failure: the service defines cancelling
		// an idle login as a no-op, so there is no error path to map.
		service.LoginCancel()
		return struct{}{}, nil
	})
	server.Handle("codex.login.device.start", func(_ context.Context, payload json.RawMessage) (any, error) {
		if err := decodeCommand(payload); err != nil {
			return nil, err
		}
		view, err := service.DeviceLoginStart()
		if err != nil {
			// Same failure code as the browser start: the frontend maps
			// both flows' start failures through one error copy, and the
			// message is the application's own text, no token material.
			return nil, platform.MethodError{Code: "codex_login_failed", Message: err.Error()}
		}
		return DeviceLoginStartResult{
			UserCode:            view.UserCode,
			VerificationURL:     view.VerificationURL,
			PollIntervalSeconds: view.PollIntervalSeconds,
		}, nil
	})
	server.Handle("codex.import.json", func(ctx context.Context, payload json.RawMessage) (any, error) {
		text, err := decodeImportText(payload)
		if err != nil {
			return nil, err
		}
		result, err := service.ImportJSON(ctx, text)
		if err != nil {
			return nil, importError(err)
		}
		return importResult(result), nil
	})
	server.Handle("codex.import.files", func(ctx context.Context, payload json.RawMessage) (any, error) {
		paths, err := decodeImportPaths(payload)
		if err != nil {
			return nil, err
		}
		result, err := service.ImportFiles(ctx, paths)
		if err != nil {
			return nil, importError(err)
		}
		return importResult(result), nil
	})
	server.Handle("codex.status", func(_ context.Context, payload json.RawMessage) (any, error) {
		if err := decodeCommand(payload); err != nil {
			return nil, err
		}
		return statusResult(service.Status()), nil
	})
	server.Handle("codex.quota", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			// accountId names the account whose usage is probed. Every
			// quota card owns one account, so the probe is per-account by
			// construction; the field is optional only because a shell
			// that predates accounts still sends an empty payload, which
			// the service answers with its not-signed-in verdict.
			AccountID string `json:"accountId,omitempty"`
		}
		if err := platform.DecodePayload(payload, &command); err != nil {
			return nil, invalidPayload()
		}
		// The probe rides the command's context, so a shell that aborts
		// the frame also aborts the HTTP call it was waiting on. The
		// answer pushes nowhere: the caller is the only surface that
		// asked, and if the probe rotated the access token the service's
		// own change feed — the one subscription below — already pushed
		// codex.changed for it.
		return quotaResult(command.AccountID, service.RefreshQuota(ctx, command.AccountID)), nil
	})
	server.Handle("codex.logout", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			// remove is the delete path: the Codex provider entry is
			// removed from the registry, not parked for a re-sign-in.
			// It stays optional — an empty payload disconnects, the same
			// command it has always been.
			Remove bool `json:"remove,omitempty"`
			// accountId targets one account's session; absent means
			// every account — the disconnect-all the command has always
			// been. The service treats an unknown id as a no-op.
			AccountID string `json:"accountId,omitempty"`
		}
		if err := platform.DecodePayload(payload, &command); err != nil {
			return nil, invalidPayload()
		}
		// Cancellation rides the request's context, so a shell that aborts
		// the command also aborts the store clear it was waiting on.
		if err := service.Logout(ctx, command.AccountID, command.Remove); err != nil {
			return nil, platform.MethodError{Code: "codex_logout_failed", Message: err.Error()}
		}
		return struct{}{}, nil
	})
	// One Register call is one subscription: surfaces refetch on
	// codex.changed instead of polling, and the listener may fire from any
	// goroutine — the server serializes its writes, so Emit is safe there.
	service.OnChanged(func(snapshot application.Snapshot) {
		_ = server.Emit("codex.changed", changedPayload(snapshot))
	})
}

// decodeCommand accepts only an empty payload object: every codex
// command except logout and quota takes no fields, so whatever a client
// puts there is unexpected by definition and gets the standard
// invalid_payload refusal instead of being silently ignored. logout and
// quota decode their own payloads — logout carries the optional remove
// flag and account id, quota the account it probes.
func decodeCommand(payload json.RawMessage) error {
	var command struct{}
	if err := platform.DecodePayload(payload, &command); err != nil {
		return invalidPayload()
	}
	return nil
}

// invalidPayload is the shared refusal for a command frame whose payload
// is not the empty object the commands declare.
func invalidPayload() platform.MethodError {
	return platform.MethodError{Code: "invalid_payload", Message: "codex command payload is invalid"}
}

const (
	// maxImportTextBytes bounds pasted credential text. Real auth JSON is
	// a few kilobytes, so 64 KiB refuses a runaway paste at the boundary
	// with the same error the service gives an unusable file — long
	// before the parser spends time on it.
	maxImportTextBytes = 64 * 1024
	// maxImportPaths caps how many files one import may name. The flow
	// stops at the first file that yields a session, so the cap exists
	// for the failure case: the all-failed summary stays readable and
	// the per-file reads stay bounded.
	maxImportPaths = 16
)

// decodeImportText accepts only {"text": ...}: strictly one field, decoded
// under DisallowUnknownFields like every other payload in the protocol.
// The text is trimmed of the whitespace a paste drags in — leading spaces
// would otherwise disguise the JSON the parser keys on — and anything
// empty or oversized is the import's own refusal, not a generic
// invalid_payload: the dialog maps codex_import_failed to import copy.
func decodeImportText(payload json.RawMessage) (string, error) {
	var command struct {
		Text string `json:"text"`
	}
	if err := platform.DecodePayload(payload, &command); err != nil {
		return "", invalidImportPayload()
	}
	if len(command.Text) > maxImportTextBytes {
		return "", invalidImportPayload()
	}
	text := strings.TrimSpace(command.Text)
	if text == "" {
		return "", invalidImportPayload()
	}
	return text, nil
}

// decodeImportPaths accepts only {"paths": [...]}. Paths are addresses,
// not content: they are validated as non-empty but never rewritten, so a
// path with an accidental leading space fails where it should — at the
// read — instead of silently pointing somewhere the user did not name.
func decodeImportPaths(payload json.RawMessage) ([]string, error) {
	var command struct {
		Paths []string `json:"paths"`
	}
	if err := platform.DecodePayload(payload, &command); err != nil {
		return nil, invalidImportPayload()
	}
	if len(command.Paths) < 1 || len(command.Paths) > maxImportPaths {
		return nil, invalidImportPayload()
	}
	for _, path := range command.Paths {
		if strings.TrimSpace(path) == "" {
			return nil, invalidImportPayload()
		}
	}
	return command.Paths, nil
}

// invalidImportPayload is the import commands' own payload refusal. It
// shares a code with import failures on purpose: the dialog has one copy
// for the whole flow, and a payload that is not importable text is an
// import that cannot be attempted.
func invalidImportPayload() platform.MethodError {
	return platform.MethodError{Code: "codex_import_failed", Message: "codex import payload is invalid"}
}

// importError maps a service import failure to the wire. The message is
// the application's own text — already account-shaped, never tokens.
func importError(err error) platform.MethodError {
	return platform.MethodError{Code: "codex_import_failed", Message: err.Error()}
}

// importResult lifts the application's import outcome onto the wire.
func importResult(result application.ImportResult) ImportResult {
	return ImportResult{
		State:        string(result.Status.State),
		Accounts:     accountResults(result.Status.Accounts),
		ImportedFrom: result.ImportedFrom,
	}
}

// statusResult lifts the connection status onto the wire: the aggregate
// state plus one row per account, in the service's order.
func statusResult(status application.Status) StatusResult {
	return StatusResult{
		State:    string(status.State),
		Accounts: accountResults(status.Accounts),
	}
}

// accountResults lifts every account row onto the wire. The slice is
// always allocated, so JSON carries an empty array rather than null —
// a status before any sign-in reads as "no accounts", not as a field
// the pane cannot tell apart from a missing one.
func accountResults(accounts []application.AccountStatus) []AccountResult {
	rows := make([]AccountResult, 0, len(accounts))
	for _, account := range accounts {
		rows = append(rows, AccountResult{
			AccountID:  account.AccountID,
			Email:      account.Email,
			Plan:       account.Plan,
			State:      string(account.State),
			ProviderID: account.ProviderID,
		})
	}
	return rows
}

// quotaResult lifts one account's settled probe onto the wire. A zero
// FetchedAt — no probe has settled — keeps the quota block absent, and
// the probe's failure text travels as a result field the card can act
// on, never as a protocol error.
func quotaResult(accountID string, snapshot application.QuotaSnapshot) QuotaResult {
	result := QuotaResult{
		AccountID: accountID,
		Error:     snapshot.Err,
	}
	if snapshot.FetchedAt != 0 {
		result.Quota = &QuotaReport{
			FetchedAt: snapshot.FetchedAt,
			PlanType:  snapshot.Usage.PlanType,
			Primary:   quotaWindowResult(snapshot.Usage.Primary),
			Secondary: quotaWindowResult(snapshot.Usage.Secondary),
		}
	}
	return result
}

// quotaWindowResult keeps only what a meter can draw: the presence, the
// remaining share, and the width and deadline when the endpoint named
// them. Percentages and epochs carry no secrets, so they cross as-is.
func quotaWindowResult(window domain.QuotaWindow) QuotaWindowResult {
	result := QuotaWindowResult{
		Present:          window.Present,
		RemainingPercent: window.RemainingPercent,
	}
	if window.WindowMinutes > 0 {
		result.WindowMinutes = window.WindowMinutes
	}
	if !window.ResetAt.IsZero() {
		result.ResetAt = window.ResetAt.Unix()
	}
	return result
}

// changedPayload flattens a snapshot for the codex.changed push. The
// map stays dumb — the login phase, the aggregate state and the account
// rows — because listeners do nothing beyond refetching; the values are
// phase text and account labels, never session tokens.
func changedPayload(snapshot application.Snapshot) map[string]any {
	payload := map[string]any{
		"loginPhase": string(snapshot.Login.Phase),
		"state":      string(snapshot.Conn.State),
		"accounts":   accountResults(snapshot.Conn.Accounts),
	}
	if snapshot.Login.Err != "" {
		payload["loginError"] = snapshot.Login.Err
	}
	return payload
}
