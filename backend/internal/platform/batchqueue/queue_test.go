package batchqueue

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestCloseDrainsAcceptedItems(t *testing.T) {
	var mu sync.Mutex
	flushed := []int{}
	queue := New(8, 4, time.Hour, func(batch []int) error {
		mu.Lock()
		flushed = append(flushed, batch...)
		mu.Unlock()
		return nil
	}, nil)
	for value := 0; value < 6; value++ {
		if !queue.Add(value) {
			t.Fatal("queue rejected an in-capacity item")
		}
	}
	if err := queue.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(flushed, []int{0, 1, 2, 3, 4, 5}) {
		t.Fatalf("unexpected flush: %v", flushed)
	}
}

func TestFailedFlushIsRetriedWithoutDroppingAcceptedItems(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	flushed := []int{}
	queue := New(8, 4, time.Millisecond, func(batch []int) error {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts < 3 {
			return context.DeadlineExceeded
		}
		flushed = append(flushed, batch...)
		return nil
	}, nil)
	for value := range 6 {
		if !queue.Add(value) {
			t.Fatal("queue rejected an in-capacity item")
		}
	}
	if err := queue.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts < 3 || !slices.Equal(flushed, []int{0, 1, 2, 3, 4, 5}) {
		t.Fatalf("failed batch was lost: attempts=%d flushed=%v", attempts, flushed)
	}
}

func TestCloseCanResumeAfterTimeout(t *testing.T) {
	blocked := true
	var mu sync.Mutex
	queue := New(1, 1, time.Millisecond, func([]int) error {
		mu.Lock()
		defer mu.Unlock()
		if blocked {
			return context.DeadlineExceeded
		}
		return nil
	}, nil)
	if !queue.Add(1) {
		t.Fatal("queue rejected an in-capacity item")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := queue.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persistent failure did not honor close timeout: %v", err)
	}
	mu.Lock()
	blocked = false
	mu.Unlock()
	if err := queue.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
