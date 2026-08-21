package fetch_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sidkang/webgate/internal/fetch"
)

func TestPendingGateAllowsAddAfterWait(t *testing.T) {
	g := fetch.NewPendingGate()
	var started sync.WaitGroup
	started.Add(1)

	errCh := make(chan error, 1)
	go func() {
		started.Done()
		errCh <- g.Wait(context.Background())
	}()
	started.Wait()
	time.Sleep(20 * time.Millisecond) // waiter is blocked on count==0? count is 0 so Wait returns immediately

	// Start with work in flight before Wait:
	g2 := fetch.NewPendingGate()
	g2.Add(1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := g2.Wait(context.Background()); err != nil {
			t.Errorf("wait: %v", err)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	// Add more work while a waiter exists (forbidden with sync.WaitGroup).
	g2.Add(2)
	g2.Done()
	g2.Done()
	g2.Done()
	wg.Wait()
}

func TestPendingGateConcurrentAddWait(t *testing.T) {
	g := fetch.NewPendingGate()
	const workers = 32
	var producers sync.WaitGroup
	for i := 0; i < workers; i++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for j := 0; j < 50; j++ {
				g.Add(1)
				go func() {
					time.Sleep(time.Millisecond)
					g.Done()
				}()
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		producers.Wait()
		close(done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Wait repeatedly until producers finish and counter drains.
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timeout waiting for gate")
		case <-done:
			if err := g.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			return
		default:
			_ = g.Wait(ctx)
			time.Sleep(time.Millisecond)
		}
	}
}

func TestPendingGateContextCancel(t *testing.T) {
	g := fetch.NewPendingGate()
	g.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := g.Wait(ctx)
	if err == nil {
		t.Fatal("expected cancel")
	}
}
