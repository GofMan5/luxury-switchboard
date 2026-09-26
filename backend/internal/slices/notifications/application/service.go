package application

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/domain"
)

// historyCap bounds the feed. A notification is a few hundred bytes of already
// sanitized text; two hundred of them cover a long incident without any risk
// of the frame budget, and older ones are simply gone — the journal is the
// activity slice's job, not this one's.
const historyCap = 200

// dedupeWindow is how long one Kind with one title stays quiet: a flapping
// provider must not turn the feed into its own heartbeat. The first
// occurrence is delivered immediately; repeats within the window are dropped.
const dedupeWindow = 10 * time.Minute

var ErrInvalidNotification = errors.New("notification is invalid")

type Service struct {
	mu         sync.Mutex
	history    []domain.Notification
	deduped    map[domain.Kind]map[string]time.Time
	listeners  []func(domain.Notification)
	now        func() time.Time
	idSequence uint64
}

func NewService() *Service {
	return &Service{deduped: map[domain.Kind]map[string]time.Time{}, now: time.Now}
}

// Raise files a notification and tells every listener at once. Sources write
// their own titles and bodies; nothing here rewrites them, so what the source
// said is what the operator reads.
func (service *Service) Raise(kind domain.Kind, severity domain.Severity, title, body string) error {
	notification := domain.Notification{
		ID: "", Kind: kind, Severity: severity,
		Title: strings.TrimSpace(title), Body: strings.TrimSpace(body),
	}
	if notification.Kind == "" || !notification.Severity.Valid() || notification.Title == "" || len(notification.Title) > 160 || len(notification.Body) > 4000 {
		return ErrInvalidNotification
	}
	service.mu.Lock()
	if service.deduped[kind] == nil {
		service.deduped[kind] = map[string]time.Time{}
	}
	last, repeated := service.deduped[kind][notification.Title]
	if repeated && service.now().Sub(last) < dedupeWindow {
		service.mu.Unlock()
		return nil
	}
	service.deduped[kind][notification.Title] = service.now()
	service.idSequence++
	notification.ID = notificationID(service.idSequence)
	notification.At = service.now()
	service.history = append(service.history, notification)
	if overflow := len(service.history) - historyCap; overflow > 0 {
		service.history = append([]domain.Notification{}, service.history[overflow:]...)
	}
	listeners := append([]func(domain.Notification){}, service.listeners...)
	service.mu.Unlock()
	for _, listener := range listeners {
		listener(notification)
	}
	return nil
}

// List answers the newest notifications first.
func (service *Service) List() []domain.Notification {
	service.mu.Lock()
	defer service.mu.Unlock()
	result := make([]domain.Notification, len(service.history))
	for index, notification := range service.history {
		result[len(result)-1-index] = notification
	}
	return result
}

func (service *Service) Clear() {
	service.mu.Lock()
	service.history = nil
	service.deduped = map[domain.Kind]map[string]time.Time{}
	service.mu.Unlock()
}

// OnChanged registers a listener called with each accepted notification, after
// it has entered the history: a listener that panics has still not lost the
// record.
func (service *Service) OnChanged(listener func(domain.Notification)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}

func notificationID(sequence uint64) string {
	// A monotonic local counter, not a UUID: notifications live one session
	// and never meet another source of IDs.
	return "n" + time.Now().Format("20060102150405") + "-" + itoa(sequence)
}

func itoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return string(digits)
}
