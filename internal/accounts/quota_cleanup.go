package accounts

import (
	"context"
	"log"
	"sync"
	"time"
)

const defaultQuotaCleanupInterval = time.Minute

// QuotaCleanupScheduler removes accounts whose locally known image quota is
// exhausted. It deliberately does not call the account refresh checker.
type QuotaCleanupScheduler struct {
	store    *Store
	interval time.Duration
	start    sync.Once
}

func NewQuotaCleanupScheduler(store *Store, interval time.Duration) *QuotaCleanupScheduler {
	if interval <= 0 {
		interval = defaultQuotaCleanupInterval
	}
	return &QuotaCleanupScheduler{store: store, interval: interval}
}

func (s *QuotaCleanupScheduler) Start(ctx context.Context) {
	if s == nil || s.store == nil || ctx == nil {
		return
	}
	s.start.Do(func() { go s.run(ctx) })
}

func (s *QuotaCleanupScheduler) run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.cleanup()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

func (s *QuotaCleanupScheduler) cleanup() {
	removed, err := s.store.RemoveExhaustedAccounts()
	if err != nil {
		log.Printf("image quota cleanup failed: %v", err)
		return
	}
	if removed > 0 {
		log.Printf("image quota cleanup removed exhausted accounts=%d", removed)
	}
}
