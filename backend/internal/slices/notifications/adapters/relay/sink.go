package notificationsrelay

import (
	"github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/domain"
)

// Sink writes the relay's routing decisions into the notification feed. The
// words are written here once, so the relay reports facts and this adapter
// turns them into the sentence the operator reads.
type Sink struct{ service *application.Service }

func NewSink(service *application.Service) *Sink { return &Sink{service: service} }

// FailoverOccurred reports one request moving to the chain's next provider.
// The model is the public name the client asked for; the providers are named
// as the operator configured them — this is the owner's own screen, where
// provider names are the vocabulary.
func (sink *Sink) FailoverOccurred(fromProvider, toProvider, publicModel string) {
	if sink.service == nil || publicModel == "" || toProvider == "" {
		return
	}
	from := fromProvider
	if from == "" {
		from = "an unnamed provider"
	}
	_ = sink.service.Raise(
		domain.KindProviderFailover, domain.SeverityWarning,
		publicModel+" failed over to "+toProvider,
		"The provider serving "+publicModel+" ("+from+") answered with a verdict no retry could change, so the request moved to the next provider in its route chain.",
	)
}
