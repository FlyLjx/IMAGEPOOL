package limiters

import (
	"context"
	"sync"
)

// Gate is a dynamically resizable concurrency limiter. A zero or negative
// limit means unlimited. Waiting callers sleep on a generation channel rather
// than polling, so a configuration update or release wakes them promptly.
type Gate struct {
	mu      sync.Mutex
	limit   int
	active  int
	waiting int
	changed chan struct{}
}

func New(limit int) *Gate {
	return &Gate{limit: limit, changed: make(chan struct{})}
}

func (g *Gate) Acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	waited := false
	for {
		g.mu.Lock()
		if g.availableLocked() {
			g.active++
			g.mu.Unlock()
			return g.releaseFunc(), nil
		}
		changed := g.changed
		if !waited {
			g.waiting++
			waited = true
		}
		g.mu.Unlock()

		select {
		case <-changed:
			g.mu.Lock()
			if waited && g.waiting > 0 {
				g.waiting--
				waited = false
			}
			g.mu.Unlock()
		case <-ctx.Done():
			g.mu.Lock()
			if waited && g.waiting > 0 {
				g.waiting--
			}
			g.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

// TryAcquire reserves a slot immediately. It returns false when the current
// limit is full, allowing a caller to coordinate several resource pools.
func (g *Gate) TryAcquire() (func(), bool) {
	if g == nil {
		return func() {}, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.availableLocked() {
		return nil, false
	}
	g.active++
	return g.releaseFuncLocked(), true
}

// Wait blocks until the gate has capacity without reserving a slot. It is
// useful when a caller must acquire a different resource in the same loop.
func (g *Gate) Wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	waited := false
	for {
		g.mu.Lock()
		if g.availableLocked() {
			if waited && g.waiting > 0 {
				g.waiting--
			}
			g.mu.Unlock()
			return nil
		}
		changed := g.changed
		if !waited {
			g.waiting++
			waited = true
		}
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			g.mu.Lock()
			if waited && g.waiting > 0 {
				g.waiting--
			}
			g.mu.Unlock()
			return ctx.Err()
		}
	}
}

func (g *Gate) SetLimit(limit int) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.limit == limit {
		g.mu.Unlock()
		return
	}
	g.limit = limit
	g.signalLocked()
	g.mu.Unlock()
}

func (g *Gate) Limit() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}

type Stats struct {
	Limit   int `json:"limit"`
	Active  int `json:"active"`
	Waiting int `json:"waiting"`
}

func (g *Gate) Stats() Stats {
	if g == nil {
		return Stats{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return Stats{Limit: g.limit, Active: g.active, Waiting: g.waiting}
}

func (g *Gate) availableLocked() bool {
	return g.limit <= 0 || g.active < g.limit
}

func (g *Gate) releaseFunc() func() {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releaseFuncLocked()
}

func (g *Gate) releaseFuncLocked() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.active > 0 {
				g.active--
			}
			g.signalLocked()
			g.mu.Unlock()
		})
	}
}

func (g *Gate) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}
