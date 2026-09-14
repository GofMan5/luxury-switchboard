package activity

import (
	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	activitydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"time"
)

type Recorder struct {
	service *activityapp.Service
}

func NewRecorder(service *activityapp.Service) *Recorder {
	return &Recorder{service: service}
}

func (recorder *Recorder) Begin(value relayapp.ActivityStart) string {
	return recorder.service.Start(activitydomain.Start{
		Model: value.Model, ProviderID: value.ProviderID, ProviderName: value.ProviderName,
		Method: value.Method, Path: value.Path, BytesIn: value.BytesIn,
	})
}

func (recorder *Recorder) Waiting(id string) {
	recorder.service.Waiting(id)
}

func (recorder *Recorder) Resume(id string, waited time.Duration) {
	recorder.service.Resume(id, waited)
}

func (recorder *Recorder) Retry(id string, value relayapp.ActivityRetry) {
	recorder.service.Retry(id, activitydomain.Retry{
		Attempt: value.Attempt, Status: value.Status, Delay: value.Delay,
	})
}

func (recorder *Recorder) Finish(id string, value relayapp.ActivityFinish) {
	recorder.service.Finish(id, activitydomain.Finish{
		Status: value.Status, BytesOut: value.BytesOut,
		Cancelled: value.Cancelled, ErrorCode: value.ErrorCode,
		ErrorDetail: value.ErrorDetail,
		InputTokens: value.Usage.InputTokens, OutputTokens: value.Usage.OutputTokens,
		CachedTokens: value.Usage.CachedTokens, ReasoningTokens: value.Usage.ReasoningTokens,
		TotalTokens: value.Usage.TotalTokens, ContextTokens: value.Usage.ContextTokens,
		Generation: value.Generation,
	})
}
