package batchqueue

import (
	"context"
	"sync"
	"time"
)

type Queue[T any] struct {
	items       chan T
	batchSize   int
	flush       func([]T) error
	maintenance func()
	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.RWMutex
	closed      bool
}

func New[T any](capacity, batchSize int, interval time.Duration, flush func([]T) error, maintenance func()) *Queue[T] {
	ctx, cancel := context.WithCancel(context.Background())
	queue := &Queue[T]{items: make(chan T, capacity), batchSize: batchSize, flush: flush, maintenance: maintenance, cancel: cancel, done: make(chan struct{})}
	go queue.run(ctx, interval)
	return queue
}

func (queue *Queue[T]) Add(value T) bool {
	queue.mu.RLock()
	defer queue.mu.RUnlock()
	if queue.closed {
		return false
	}
	select {
	case queue.items <- value:
		return true
	default:
		return false
	}
}

func (queue *Queue[T]) Close(ctx context.Context) error {
	queue.mu.Lock()
	if !queue.closed {
		queue.closed = true
		queue.cancel()
	}
	queue.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-queue.done:
		return nil
	}
}

func (queue *Queue[T]) run(ctx context.Context, interval time.Duration) {
	defer close(queue.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	batch := make([]T, 0, queue.batchSize)
	lastMaintenance := time.Now()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = queue.flush(batch)
		batch = batch[:0]
		if queue.maintenance != nil && time.Since(lastMaintenance) >= time.Hour {
			queue.maintenance()
			lastMaintenance = time.Now()
		}
	}
	for {
		select {
		case value := <-queue.items:
			batch = append(batch, value)
			if len(batch) >= queue.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case value := <-queue.items:
					batch = append(batch, value)
				default:
					flush()
					return
				}
			}
		}
	}
}
