package application

import (
	"github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/domain"
	"time"
)

type ClientStart struct {
	IP          string
	Method      string
	Path        string
	PublicModel string
	BytesIn     int64
}
type ClientFinish struct {
	Status    int
	BytesOut  int64
	ErrorCode string
	Duration  time.Duration
}
type ClientActivity interface {
	Queue(string, int)
	Begin(ClientStart) string
	Finish(string, ClientFinish)
}

type Routes interface {
	List() []domain.Route
	Resolve(string) (domain.Route, bool)
}

type Markers interface {
	SensitiveMarkers(string) []string
}
