package accounts

import (
	"context"
	"errors"
	"strings"
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
