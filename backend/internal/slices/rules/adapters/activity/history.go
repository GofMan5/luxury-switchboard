// Package activity reads failed requests from the activity history for
// rules.recentErrors.
package activity

import (
	"context"
	"errors"

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	activitydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/application"
)

// scanLimit is the most rows the history answers in one read; the failures
// among them are what recentErrors groups.
const scanLimit = 500

type History struct{ history activityapp.History }

// New wraps history. A nil history makes every read fail: unavailable
// history is not an empty list.
func New(history activityapp.History) *History { return &History{history: history} }

var _ application.FailureHistory = (*History)(nil)

func (adapter *History) RecentFailures(ctx context.Context) ([]application.FailureRecord, error) {
	if adapter == nil || adapter.history == nil {
		return nil, errors.New("request history is unavailable")
	}
	requests, err := adapter.history.Recent(ctx, activityapp.PeriodAll, scanLimit)
	if err != nil {
		return nil, err
	}
	failures := make([]application.FailureRecord, 0, len(requests))
	for _, request := range requests {
		if request.State != activitydomain.StateFailed {
			continue
		}
		failures = append(failures, application.FailureRecord{
			At:           request.UpdatedAt,
			Status:       request.Status,
			ErrorCode:    request.ErrorCode,
			Detail:       request.ErrorDetail,
			ProviderID:   request.ProviderID,
			ProviderName: request.ProviderName,
			Model:        request.Model,
		})
	}
	return failures, nil
}
