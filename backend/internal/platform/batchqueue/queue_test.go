package batchqueue

import (
	"context"
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
