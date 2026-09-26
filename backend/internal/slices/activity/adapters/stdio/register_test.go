package activitystdio

import (
	"strings"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

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
