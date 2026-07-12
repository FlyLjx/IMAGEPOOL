package accounts

import (
	"context"
	"log"
	"sync"
	"time"
)

// AutoRefreshScheduler refreshes every account after the configured interval.
// It runs refreshes synchronously so a slow upstream cannot overlap the next run.
type AutoRefreshScheduler struct {
	store   *Store
	refresh *RefreshManager

	mu       sync.RWMutex
	interval int
	changed  chan struct{}
	start    sync.Once
	duration func(int) time.Duration
}

func NewAutoRefreshScheduler(store *Store, refresh *RefreshManager, intervalMinutes int) *AutoRefreshScheduler {
	return &AutoRefreshScheduler{
		store:    store,
		refresh:  refresh,
		interval: normalizeRefreshInterval(intervalMinutes),
		changed:  make(chan struct{}, 1),
		duration: func(minutes int) time.Duration { return time.Duration(minutes) * time.Minute },
	}
}

func (s *AutoRefreshScheduler) Start(ctx context.Context) {
	if s == nil || s.store == nil || s.refresh == nil || ctx == nil {
		return
	}
	s.start.Do(func() {
		go s.run(ctx)
	})
}

func (s *AutoRefreshScheduler) UpdateInterval(intervalMinutes int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.interval = normalizeRefreshInterval(intervalMinutes)
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *AutoRefreshScheduler) run(ctx context.Context) {
	for {
		timer := time.NewTimer(s.waitDuration())
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-s.changed:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
			s.refreshAll()
		}
	}
}

func (s *AutoRefreshScheduler) waitDuration() time.Duration {
	s.mu.RLock()
	minutes := s.interval
	duration := s.duration
	s.mu.RUnlock()
	return duration(minutes)
}

func (s *AutoRefreshScheduler) refreshAll() {
	tokens := s.store.Tokens()
	if len(tokens) == 0 {
		log.Printf("automatic account refresh skipped: no accounts")
		return
	}
	progress, err := s.refresh.RefreshNow(tokens)
	if err != nil {
		log.Printf("automatic account refresh failed: %v", err)
		return
	}
	log.Printf("automatic account refresh completed: total=%d success=%d removed=%d errors=%d quota=%d", progress.Total, progress.StatusCounts["success"], progress.StatusCounts["removed"], progress.StatusCounts["error"], progress.TotalQuota)
}

func normalizeRefreshInterval(intervalMinutes int) int {
	if intervalMinutes <= 0 {
		return 5
	}
	if intervalMinutes > 525600 {
		return 525600
	}
	return intervalMinutes
}
