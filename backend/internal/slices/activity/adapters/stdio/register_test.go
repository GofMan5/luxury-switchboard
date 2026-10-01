package activitystdio

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

// A History that answers from a fixed slice: the stdio contract is what is under
// test, not the storage.
type fakeHistory struct{ requests []domain.Request }

func (history *fakeHistory) Record(domain.Request) bool { return true }
func (history *fakeHistory) Recent(_ context.Context, _ application.Period, limit int) ([]domain.Request, error) {
	if limit > len(history.requests) {
		limit = len(history.requests)
	}
	return history.requests[:limit], nil
}
func (history *fakeHistory) Stats(context.Context, application.Period) (application.HistoryStats, error) {
	return application.HistoryStats{}, nil
}
func (history *fakeHistory) Close(context.Context) error { return nil }

// The Insights workspace reads the same rows this handler serves, and a
// provider incident fills them with the 4096-rune error detail the activity
// list already had to be bounded against — measured there at 351 KB for a
// hundred rows. The Insights leg answered `response_too_large` instead, at
// exactly the moment the operator needed to see what was failing, and the
// workspace could not lower the limit it asked with. The handler now answers
// under the same payload budget, and says how much it held back.
func TestTheHistoryTheWorkspaceAsksForFitsInOneProtocolFrame(t *testing.T) {
	detail := strings.Repeat("x", 3000)
	requests := make([]domain.Request, 100)
	for index := range requests {
		requests[index] = domain.Request{
			ID: "req" + strconv.Itoa(index), State: domain.StateFailed,
			Model: "gpt-6-astra", Method: "POST", Path: "/v1/responses",
			Status: 502, ErrorCode: "upstream_status", ErrorDetail: detail,
		}
	}
	input := `{"v":1,"id":"req0","type":"command","method":"history.recent","payload":{"period":"24h","limit":100}}` + "\n"
	var output strings.Builder
	server := platform.NewServer(strings.NewReader(input), &output, 1)
	Register(server, application.NewService(100), &fakeHistory{requests: requests})
	if err := server.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var frame map[string]any
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(output.String())))
	decoder.UseNumber()
	if decoder.Decode(&frame) != nil {
		t.Fatalf("unreadable frame: %s", output.String())
	}
	if frame["ok"] != true {
		t.Fatalf("history.recent failed: %s", output.String())
	}
	payload, _ := frame["payload"].(map[string]any)
	rows, _ := payload["requests"].([]any)
	if len(rows) == 0 || len(rows) >= len(requests) {
		t.Fatalf("the bound did not shrink the journal: %d of %d", len(rows), len(requests))
	}
	if served, err := payload["available"].(json.Number); err == false || served.String() != strconv.Itoa(len(requests)) {
		t.Fatalf("the untruncated total was lost: %v", payload["available"])
	}
	// The bytes the desktop shell would read, one frame per line: the refusal
	// it answers to an oversized frame kills the sidecar's read loop.
	if widest := len(strings.TrimSpace(output.String())) + 1; widest > platform.MaxFrameBytes {
		t.Fatalf("the answer is %d bytes, over the %d the shell accepts", widest, platform.MaxFrameBytes)
	}
}

// A provider incident fills the rows with the raw error bodies the history now
// carries. Measured: one hundred such rows are 351 KB against the 258 KB
// payload budget, and the workspace answered `response_too_large` at exactly
// the moment it was needed. The bound keeps the newest rows and never answers
// empty because the newest one alone did not fit.
func TestALargeActivityListFitsInOneProtocolFrame(t *testing.T) {
	service := application.NewService(2_000)
	detail := strings.Repeat("x", 3_000)
	for range 100 {
		id := service.Start(domain.Start{Model: "gpt-6-astra", Method: "POST", Path: "/v1/responses"})
		service.Finish(id, domain.Finish{Status: 403, ErrorCode: "request_rejected", ErrorDetail: detail})
	}
	requests := service.List(100)
	bounded := withinOneFrame(requests)
	if len(bounded) >= len(requests) || len(bounded) == 0 {
		t.Fatalf("the bound did not shrink the list: %d of %d", len(bounded), len(requests))
	}
	for index := range bounded {
		if bounded[index].ID != requests[index].ID {
			t.Fatalf("the bound kept stale rows: position %d", index)
		}
	}
	// The newest row survives alone when everything else is dropped.
	single := withinOneFrame(requests[:1])
	if len(single) != 1 {
		t.Fatalf("a single row was dropped: %d", len(single))
	}
}
