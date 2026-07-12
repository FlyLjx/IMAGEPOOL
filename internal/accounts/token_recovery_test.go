package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"imagepool/internal/config"
)

type tokenRecoveryRefresher struct {
	accessToken  string
	refreshToken string
	idToken      string
	err          error
	calls        int
}

func (r *tokenRecoveryRefresher) RefreshToken(ctx context.Context, refreshToken string) (string, string, string, error) {
	r.calls++
	if r.err != nil {
		return "", "", "", r.err
	}
	return r.accessToken, r.refreshToken, r.idToken, nil
}

type tokenRecoveryChecker struct {
	result AccountCheckResult
	err    error
	tokens []string
}

type tokenRecoveryRelogger struct {
	accessToken  string
	refreshToken string
	idToken      string
	err          error
	calls        int
	email        string
	password     string
}

func (r *tokenRecoveryRelogger) ReLogin(ctx context.Context, email, password string) (string, string, string, error) {
	r.calls++
	r.email = email
	r.password = password
	if r.err != nil {
		return "", "", "", r.err
	}
	return r.accessToken, r.refreshToken, r.idToken, nil
}

func (c *tokenRecoveryChecker) CheckAccount(ctx context.Context, token string) (AccountCheckResult, error) {
	c.tokens = append(c.tokens, token)
	if c.err != nil {
		return AccountCheckResult{}, c.err
	}
	return c.result, nil
}

func TestTokenRecoveryRestoresAccountAfterRefreshingOAuthToken(t *testing.T) {
	now := time.Date(2026, 7, 12, 13, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	store := NewStore([]Account{{Email: "old@example.test", AccessToken: "old", RefreshToken: "refresh", IDToken: "old-id", Status: "正常", Extra: map[string]any{}}}, "")
	store.now = func() time.Time { return now }
	if _, queued, err := store.MarkTokenRecoveryPending("old", "upstream status=401"); err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}

	cfg := config.Default()
	cfg.TokenRecoveryConcurrency = 1
	checker := &tokenRecoveryChecker{result: AccountCheckResult{Email: "fresh@example.test", ImageQuotaUnknown: true, Models: []string{"gpt-image-2"}}}
	refresher := &tokenRecoveryRefresher{accessToken: "new", refreshToken: "rotated", idToken: "new-id"}
	manager := NewTokenRecoveryManager(cfg, store, checker, refresher)
	manager.now = func() time.Time { return now }
	manager.RecoverDue(context.Background())

	if _, found := store.Get("old"); found {
		t.Fatalf("old token remained after recovery: %#v", store.List())
	}
	account, found := store.Get("new")
	if !found || account.Status != "正常" || account.RefreshToken != "rotated" || account.IDToken != "new-id" || account.Email != "fresh@example.test" {
		t.Fatalf("recovered account=%#v found=%v", account, found)
	}
	if _, pending := account.Extra[tokenRecoveryStateKey]; pending || refresher.calls != 1 || len(checker.tokens) != 1 || checker.tokens[0] != "new" {
		t.Fatalf("account=%#v refresher=%#v checker=%#v", account, refresher, checker)
	}
	selected, err := store.SelectForImage(nil)
	if err != nil || selected.AccessToken != "new" {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}
	logs := store.CredentialRecoveryLogs("", 20)
	if len(logs) != 4 || logs[0].Event != "recovery_succeeded" || logs[1].Event != "token_refreshed" || logs[2].Event != "recovery_started" || logs[3].Event != "credential_invalid" {
		t.Fatalf("recovery logs=%#v", logs)
	}
}

func TestTokenRecoveryDeletesAfterConfiguredFailures(t *testing.T) {
	now := time.Date(2026, 7, 12, 13, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	store := NewStore([]Account{{AccessToken: "old", RefreshToken: "refresh", Status: "正常", Extra: map[string]any{}}}, "")
	store.now = func() time.Time { return now }
	if _, queued, err := store.MarkTokenRecoveryPending("old", "token_revoked"); err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}

	cfg := config.Default()
	cfg.TokenRecoveryConcurrency = 1
	cfg.TokenRecoveryIntervalSecs = 1
	cfg.TokenRecoveryMaxAttempts = 3
	manager := NewTokenRecoveryManager(cfg, store, &tokenRecoveryChecker{}, &tokenRecoveryRefresher{err: errors.New("invalid_grant")})
	manager.now = func() time.Time { return now }
	for attempt := 1; attempt < cfg.TokenRecoveryMaxAttempts; attempt++ {
		manager.RecoverDue(context.Background())
		account, found := store.Get("old")
		if !found || account.Status != StatusCredentialInvalid || asInt(account.Extra[tokenRecoveryAttemptsKey]) != attempt {
			t.Fatalf("after failure %d account=%#v found=%v", attempt, account, found)
		}
		now = now.Add(time.Second)
	}
	manager.RecoverDue(context.Background())
	if _, found := store.Get("old"); found {
		t.Fatalf("account remained after configured recovery failures: %#v", store.List())
	}
	logs := store.CredentialRecoveryLogs("", 20)
	if len(logs) == 0 || logs[0].Event != "recovery_deleted" || logs[0].Attempt != cfg.TokenRecoveryMaxAttempts || logs[0].Error != "invalid_grant" {
		t.Fatalf("deletion recovery logs=%#v", logs)
	}
}

func TestTokenRecoveryFallsBackToPasswordLoginForInvalidatedRefreshToken(t *testing.T) {
	now := time.Date(2026, 7, 12, 13, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	store := NewStore([]Account{{Email: "person@example.test", Password: "saved-password", AccessToken: "old", RefreshToken: "refresh", IDToken: "old-id", Status: "正常", Extra: map[string]any{}}}, "")
	store.now = func() time.Time { return now }
	if _, queued, err := store.MarkTokenRecoveryPending("old", "token_revoked"); err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}

	refresher := &tokenRecoveryRefresher{err: errors.New("oauth token refresh rejected (HTTP 401): refresh_token_invalidated")}
	relogger := &tokenRecoveryRelogger{accessToken: "fresh", refreshToken: "fresh-refresh", idToken: "fresh-id"}
	checker := &tokenRecoveryChecker{result: AccountCheckResult{Email: "person@example.test", ImageQuotaUnknown: true}}
	manager := NewTokenRecoveryManager(config.Default(), store, checker, refresher, relogger)
	manager.now = func() time.Time { return now }
	manager.RecoverDue(context.Background())

	account, found := store.Get("fresh")
	if !found || account.Status != "正常" || account.RefreshToken != "fresh-refresh" || refresher.calls != 1 || relogger.calls != 1 || relogger.email != "person@example.test" || relogger.password != "saved-password" {
		t.Fatalf("account=%#v found=%v refresher=%#v relogger=%#v", account, found, refresher, relogger)
	}
	logs := store.CredentialRecoveryLogs("person@example.test", 20)
	events := make([]string, 0, len(logs))
	for _, entry := range logs {
		events = append(events, entry.Event)
	}
	want := []string{"recovery_succeeded", "password_relogin_succeeded", "password_relogin_started", "refresh_token_invalidated", "recovery_started", "credential_invalid"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events=%#v want=%#v", events, want)
	}
}
