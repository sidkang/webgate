package fetch

import (
	"context"
	"sync"
)

// PendingGate counts in-flight work and allows Add after Waiters exist
// (unlike sync.WaitGroup). Wait returns when count reaches 0 or ctx is done.
type PendingGate struct {
	mu    sync.Mutex
	cond  *sync.Cond
	count int
}

// NewPendingGate returns an empty gate.
func NewPendingGate() *PendingGate {
	g := &PendingGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// Add adjusts the in-flight counter by delta (may be called after Waiters).
func (g *PendingGate) Add(delta int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.count += delta
	if g.count < 0 {
		panic("PendingGate: negative counter")
	}
	if g.count == 0 {
		g.cond.Broadcast()
	}
}

// Done decrements the counter by one.
func (g *PendingGate) Done() {
	g.Add(-1)
}

// Wait blocks until count is 0 or ctx is cancelled.
func (g *PendingGate) Wait(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		g.cond.Broadcast()
		g.mu.Unlock()
	})
	defer stop()

	g.mu.Lock()
	defer g.mu.Unlock()
	for g.count > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.cond.Wait()
	}
	return nil
}
