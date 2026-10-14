package application

import (
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

func TestRetryCountsEventsWhenNestedAttemptNumbersRestart(t *testing.T) {
	service := NewService(10)
	id := service.Start(domain.Start{Path: "/v1/responses"})
	for range 2 {
		service.Retry(id, domain.Retry{Attempt: 1, Status: 502, Delay: time.Millisecond})
	}
	row := service.List(1)[0]
	if row.Retries != 2 || row.QueueMS != 2 || row.State != domain.StateRetrying {
		t.Fatalf("two retry events reused attempt 1: %+v", row)
	}
	service.Finish(id, domain.Finish{Status: 200})
	if row := service.List(1)[0]; row.Retries != 2 || row.State != domain.StateCompleted {
		t.Fatalf("completed request lost retry count: %+v", row)
	}
}
