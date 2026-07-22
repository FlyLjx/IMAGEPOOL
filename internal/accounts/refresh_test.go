package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type refreshChecker struct{ errors map[string]error }

func (c refreshChecker) CheckAccount(ctx context.Context, token string) (AccountCheckResult, error) {
	if err := c.errors[token]; err != nil {
		return AccountCheckResult{}, err
	}
	return AccountCheckResult{Models: []string{"gpt-5-5"}, ImageQuotaUnknown: true}, nil
}

type notifyingRefreshChecker struct{ called chan<- struct{} }

func (c notifyingRefreshChecker) CheckAccount(ctx context.Context, token string) (AccountCheckResult, error) {
	select {
	case c.called <- struct{}{}:
	default:
	}
	return AccountCheckResult{Models: []string{"gpt-5-5"}}, nil
}

func TestAutoRefreshSchedulerResetsAfterIntervalUpdate(t *testing.T) {
	called := make(chan struct{}, 1)
	store := NewStore([]Account{{Email: "ok@example", AccessToken: "ok"}}, "")
	manager := NewRefreshManager(store, notifyingRefreshChecker{called: called}, 1)
	scheduler := NewAutoRefreshScheduler(store, manager, 1)
	scheduler.duration = func(minutes int) time.Duration {
		if minutes == 1 {
			return time.Hour
		}
		return time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	scheduler.UpdateInterval(2)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("automatic refresh did not run after interval update")
	}
}

func TestRefreshManagerRemovesAuthenticationFailures(t *testing.T) {
	store := NewStore([]Account{{Email: "ok@example", AccessToken: "ok"}, {Email: "bad@example", AccessToken: "bad"}}, "")
	manager := NewRefreshManager(store, refreshChecker{errors: map[string]error{"bad": errors.New("token_revoked")}}, 2)
	id, err := manager.Start([]string{"ok", "bad"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		progress, ok := manager.Get(id)
		if ok && progress.Done {
			_, found := store.Get("bad")
			if progress.Processed != 2 || progress.StatusCounts["removed"] != 1 || found {
				t.Fatalf("progress=%#v accounts=%#v", progress, store.List())
			}
			return
		}
		time.Sleep(time.Millisecond * 10)
	}
	t.Fatal("refresh did not finish")
}

func TestRefreshNowValidatesBeforeReturning(t *testing.T) {
	store := NewStore([]Account{{Email: "ok@example", AccessToken: "ok"}, {Email: "bad@example", AccessToken: "bad"}}, "")
	manager := NewRefreshManager(store, refreshChecker{errors: map[string]error{"bad": errors.New("token_invalidated")}}, 2)
	progress, err := manager.RefreshNow([]string{"ok", "bad"})
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Done || progress.Processed != 2 || progress.StatusCounts["success"] != 1 || progress.StatusCounts["removed"] != 1 {
		t.Fatalf("progress=%#v", progress)
	}
	items := store.List()
	if len(items) != 1 {
		t.Fatalf("accounts=%#v", items)
	}
	if _, found := store.Get("bad"); found {
		t.Fatalf("accounts=%#v", items)
	}
}

func TestRecordRefreshClearsStaleUnknownQuotaFlag(t *testing.T) {
	store := NewStore([]Account{{AccessToken: "token", Extra: map[string]any{"image_quota_unknown": true}}}, "")
	account, found, err := store.RecordRefresh("token", AccountCheckResult{Quota: 5, ImageQuotaUnknown: false}, nil)
	if err != nil || !found {
		t.Fatalf("account=%#v found=%v err=%v", account, found, err)
	}
	data, err := account.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "image_quota_unknown") {
		t.Fatalf("stale unknown quota flag remained: %s", data)
	}
}

type scheduledConcurrencyChecker struct {
	active atomic.Int32
	max    atomic.Int32
	gate   <-chan struct{}
}

func (c *scheduledConcurrencyChecker) CheckAccount(ctx context.Context, token string) (AccountCheckResult, error) {
	return c.CheckAccountLight(ctx, token)
}

func (c *scheduledConcurrencyChecker) CheckAccountLight(ctx context.Context, token string) (AccountCheckResult, error) {
	active := c.active.Add(1)
	for {
		maximum := c.max.Load()
		if active <= maximum || c.max.CompareAndSwap(maximum, active) {
			break
		}
	}
	defer c.active.Add(-1)
	select {
	case <-c.gate:
		return AccountCheckResult{ImageQuotaUnknown: true}, nil
	case <-ctx.Done():
		return AccountCheckResult{}, ctx.Err()
	}
}

func TestScheduledRefreshCapsBackgroundConcurrency(t *testing.T) {
	accounts := make([]Account, 0, 20)
	tokens := make([]string, 0, 20)
	for index := 0; index < 20; index++ {
		token := fmt.Sprintf("token-%d", index)
		accounts = append(accounts, Account{AccessToken: token})
		tokens = append(tokens, token)
	}
	gate := make(chan struct{})
	checker := &scheduledConcurrencyChecker{gate: gate}
	manager := NewRefreshManager(NewStore(accounts, ""), checker, 20)
	done := make(chan error, 1)
	go func() {
		_, err := manager.RefreshScheduled(tokens)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for checker.max.Load() < scheduledRefreshConcurrencyLimit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if maximum := checker.max.Load(); maximum != scheduledRefreshConcurrencyLimit {
		close(gate)
		t.Fatalf("scheduled concurrency=%d", maximum)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestScheduledRefreshTokensExcludeLeasedAccounts(t *testing.T) {
	store := NewStore([]Account{{AccessToken: "leased", Status: "正常"}, {AccessToken: "idle", Status: "正常"}}, "")
	account, err := store.AcquireAccountForImage(context.Background(), "leased", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseImage(account.AccessToken)
	tokens := store.TokensForScheduledRefresh()
	if len(tokens) != 1 || tokens[0] != "idle" {
		t.Fatalf("scheduled refresh tokens=%#v", tokens)
	}
}
