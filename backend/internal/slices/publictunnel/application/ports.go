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
	// Reject reports an attempt refused at the gateway. It must stay cheaper than a
	// served request so a refused client cannot cost more than an accepted one.
	Reject(string)
}

type Routes interface {
	List() []domain.Route
	Resolve(string) (domain.Route, bool)
}

type Markers interface {
	SensitiveMarkers(string) []string
}

// Bans reports owner blocklist decisions for a client address.
type Bans interface {
	Banned(string) bool
}
