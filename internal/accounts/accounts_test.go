package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelectNewestAccountFirst(t *testing.T) {
	store := NewStore([]Account{{Email: "old@example.com", AccessToken: "old", CreatedAt: 10}, {Email: "new@example.com", AccessToken: "new", CreatedAt: 20}}, "")
	got, err := store.SelectForImage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new" {
		t.Fatalf("selected %q", got.AccessToken)
	}
	got, err = store.SelectForImage(map[string]bool{"new": true})
	if err != nil || got.AccessToken != "old" {
		t.Fatalf("exclude got=%#v err=%v", got, err)
	}
}

func TestAcquireForImageWaitsForOccupiedAccount(t *testing.T) {
	store := NewStore([]Account{{Email: "one@example.com", AccessToken: "one", CreatedAt: 1}}, "")
	first, err := store.AcquireForImage(context.Background(), nil, nil)
	if err != nil || first.AccessToken != "one" {
		t.Fatalf("first=%#v err=%v", first, err)
	}

	waiting := make(chan struct{}, 1)
	type result struct {
		account Account
		err     error
	}
	done := make(chan result, 1)
	go func() {
		account, err := store.AcquireForImage(context.Background(), nil, func() { waiting <- struct{}{} })
		done <- result{account: account, err: err}
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("second task did not enter the account queue")
	}
	select {
	case result := <-done:
		t.Fatalf("occupied account was reused: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}

	store.ReleaseImage(first.AccessToken)
	select {
	case result := <-done:
		if result.err != nil || result.account.AccessToken != "one" {
			t.Fatalf("result=%#v", result)
		}
		store.ReleaseImage(result.account.AccessToken)
	case <-time.After(time.Second):
		t.Fatal("queued task did not acquire after release")
	}
}

func TestAcquireAccountForImageWaitsForOccupiedAccount(t *testing.T) {
	store := NewStore([]Account{{Email: "one@example.com", AccessToken: "one", CreatedAt: 1}}, "")
	first, err := store.AcquireAccountForImage(context.Background(), "one", nil)
	if err != nil || first.AccessToken != "one" {
		t.Fatalf("first=%#v err=%v", first, err)
	}

	waiting := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := store.AcquireAccountForImage(context.Background(), "one", func() { waiting <- struct{}{} })
		done <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("specific account task did not enter the queue")
	}

	store.ReleaseImage(first.AccessToken)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("specific queued task did not acquire after release")
	}
}

func TestRemoveInvalidTokenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	store := NewStore([]Account{{Email: "a", AccessToken: "tok", CreatedAt: 1}}, path)
	removed, err := store.RemoveInvalidToken("tok", "token_revoked")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if _, err := store.SelectForImage(nil); !errors.Is(err, ErrNoAvailableAccount) {
		t.Fatalf("want no account got %v", err)
	}
	logs := store.CredentialRecoveryLogs("", 10)
	if len(logs) != 1 || logs[0].Event != "account_deleted" || logs[0].Error != "token_revoked" {
		t.Fatalf("logs=%#v", logs)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"accounts"`) {
		t.Fatalf("bad saved data: %s", data)
	}
}

func TestPublicAccountIncludesDerivedHealth(t *testing.T) {
	account := Account{AccessToken: "token", Status: "正常", Quota: 25, Extra: map[string]any{}}
	public := account.Public()
	if public["health_score"] != float64(100) || public["health_label"] != "优秀" {
		t.Fatalf("health=%#v", public)
	}
}

func TestMarkImageSuccessUpdatesKnownQuotaEstimate(t *testing.T) {
	store := NewStore([]Account{{
		AccessToken: "token",
		Quota:       25,
		Extra: map[string]any{
			"limits_progress": []map[string]any{{"feature_name": "image_gen", "remaining": 25}},
		},
	}}, "")
	if err := store.MarkImageSuccess("token"); err != nil {
		t.Fatal(err)
	}
	account, found := store.Get("token")
	if !found || account.Quota != 24 || account.ImageOK != 1 || asInt(account.Extra["image_quota_total"]) != 25 {
		t.Fatalf("account=%#v found=%v", account, found)
	}
	progress := account.Extra["limits_progress"].([]map[string]any)
	if asInt(progress[0]["remaining"]) != 24 {
		t.Fatalf("limits_progress=%#v", progress)
	}
}

func TestMarkImageQuotaExhaustedRetainsAccount(t *testing.T) {
	store := NewStore([]Account{{AccessToken: "token", Quota: 1, Status: "正常", Extra: map[string]any{}}}, "")
	if err := store.MarkImageQuotaExhausted("token", errors.New("no available free image quota")); err != nil {
		t.Fatal(err)
	}
	account, found := store.Get("token")
	if !found || account.Status != "限流" || account.Quota != 0 {
		t.Fatalf("account=%#v found=%v", account, found)
	}
}

func TestEnsureBrowserIdentityPersistsStableValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	store := NewStore([]Account{{AccessToken: "token"}}, path)
	first, found, err := store.EnsureBrowserIdentity("token")
	if err != nil || !found || first.DeviceID == "" || first.SessionID == "" || first.UserAgent != DefaultBrowserUserAgent {
		t.Fatalf("account=%#v found=%v err=%v", first, found, err)
	}
	second, found, err := store.EnsureBrowserIdentity("token")
	if err != nil || !found || second.DeviceID != first.DeviceID || second.SessionID != first.SessionID || second.UserAgent != first.UserAgent {
		t.Fatalf("account=%#v found=%v err=%v", second, found, err)
	}
	persisted, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found := persisted.Get("token")
	if !found || loaded.DeviceID != first.DeviceID || loaded.SessionID != first.SessionID || loaded.UserAgent != first.UserAgent {
		t.Fatalf("loaded=%#v found=%v", loaded, found)
	}
}
