package application

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/updates/domain"
	"io"
	"sync"
	"testing"
	"time"
)

type waitInstallFeed struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (feed *waitInstallFeed) LatestRelease(context.Context) (domain.Latest, error) {
	feed.once.Do(func() { close(feed.started) })
	<-feed.release
	return domain.Latest{}, errors.New("offline fixture")
}
func (*waitInstallFeed) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("unused")
}
func TestCancelledInstallDoesNotWaitForActiveInstall(t *testing.T) {
	feed := &waitInstallFeed{started: make(chan struct{}), release: make(chan struct{})}
	service := NewService("1.0.59", feed)
	first := make(chan struct{})
	go func() { defer close(first); _, _ = service.Install(context.Background(), func(InstallProgress) {}) }()
	<-feed.started
	defer func() { close(feed.release); <-first }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second := make(chan error, 1)
	go func() { _, err := service.Install(ctx, func(InstallProgress) {}); second <- err }()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("cancelled waiter blocked behind install")
	}
}
