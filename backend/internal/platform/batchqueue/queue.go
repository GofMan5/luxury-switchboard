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
	done        chan struct{}
	mu          sync.RWMutex
	closed      bool
}

func New[T any](capacity, batchSize int, interval time.Duration, flush func([]T) error, maintenance func()) *Queue[T] {
	queue := &Queue[T]{items: make(chan T, capacity), batchSize: batchSize, flush: flush, maintenance: maintenance, done: make(chan struct{})}
	go queue.run(interval)
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
		close(queue.items)
	}
	queue.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-queue.done:
		return nil
	}
}

func (queue *Queue[T]) run(interval time.Duration) {
	defer close(queue.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	batch := make([]T, 0, queue.batchSize)
	lastMaintenance := time.Now()
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		if queue.flush(batch) != nil {
			return false
		}
		batch = batch[:0]
		if queue.maintenance != nil && time.Since(lastMaintenance) >= time.Hour {
			queue.maintenance()
			lastMaintenance = time.Now()
		}
		return true
	}
	items := (<-chan T)(queue.items)
	for {
		if items == nil {
			if len(batch) == 0 || flush() {
				return
			}
		}
		intake := items
		if len(batch) >= queue.batchSize {
			intake = nil
		}
		select {
		case value, ok := <-intake:
			if !ok {
				items = nil
				continue
			}
			batch = append(batch, value)
			if len(batch) >= queue.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
