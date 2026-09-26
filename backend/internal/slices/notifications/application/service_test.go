package application

import (
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/domain"
)

func TestRaiseDeduplicatesAFlappingTitle(t *testing.T) {
	service := NewService()
	raised := 0
	service.OnChanged(func(domain.Notification) { raised++ })
	if err := service.Raise(domain.KindProviderHealth, domain.SeverityDanger, "alpha-relay is unreachable", "probe failed"); err != nil {
		t.Fatal(err)
	}
	// The same verdict flapping inside the window is one notification.
	if err := service.Raise(domain.KindProviderHealth, domain.SeverityDanger, "alpha-relay is unreachable", "probe failed again"); err != nil {
		t.Fatal(err)
	}
	if raised != 1 {
		t.Fatalf("a flapping title was not deduplicated: %d", raised)
	}
	// A different title of the same kind is a different event.
	if err := service.Raise(domain.KindProviderHealth, domain.SeverityWarning, "agent is unreachable", "probe failed"); err != nil {
		t.Fatal(err)
	}
	if raised != 2 {
		t.Fatalf("a distinct event was swallowed by the dedupe: %d", raised)
	}
}

func TestHistoryIsBoundedAndNewestFirst(t *testing.T) {
	service := NewService()
	// Distinct titles a flapping source would never produce: a counter, so
	// the newest is unambiguous and the dedupe stays out of the way.
	for index := 0; index < historyCap+25; index++ {
		title := "title-" + itoa(uint64(index)) + "-" + itoa(uint64(time.Now().UnixNano()))
		if err := service.Raise(domain.KindProviderFailover, domain.SeverityWarning, title, "body"); err != nil {
			t.Fatal(err)
		}
	}
	notifications := service.List()
	if len(notifications) != historyCap {
		t.Fatalf("history is unbounded: %d", len(notifications))
	}
	// The newest first: the last raised title ends with the counter 224.
	if !contains(notifications, "title-224-") || !contains(notifications[1:], "title-223-") {
		t.Fatal("the feed is not newest first")
	}
}

func contains(notifications []domain.Notification, prefix string) bool {
	for _, notification := range notifications {
		if len(notification.Title) >= len(prefix) && notification.Title[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func TestInvalidNotificationsAreRefused(t *testing.T) {
	service := NewService()
	if err := service.Raise("", domain.SeverityInfo, "title", "body"); err == nil {
		t.Fatal("an empty kind was accepted")
	}
	if err := service.Raise(domain.KindKeyHealth, "loud", "title", "body"); err == nil {
		t.Fatal("an unknown severity was accepted")
	}
	if err := service.Raise(domain.KindKeyHealth, domain.SeverityInfo, "   ", "body"); err == nil {
		t.Fatal("an empty title was accepted")
	}
}
