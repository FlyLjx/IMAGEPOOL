package adobe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"imagepool/internal/config"
	"imagepool/internal/persistence"
)

func TestPostgresCreditsAuthFailureRefreshesCookieAndRetries(t *testing.T) {
	runtime, cleanup := testPostgresRuntime(t)
	defer cleanup()
	ctx := context.Background()
	enabled := true
	route, err := runtime.repository.CreateRoute(ctx, RouteInput{Name: "credits-recovery", ProxyURL: "direct://", Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}

	accountID := "credits-recovery-account"
	expiresAt := time.Now().Add(time.Hour).UTC()
	oldToken := jwtWithClaims(map[string]any{"user_id": accountID, "exp": float64(expiresAt.Unix())})
	newToken := jwtWithClaims(map[string]any{"user_id": accountID, "exp": float64(expiresAt.Add(time.Hour).Unix())})
	account, err := runtime.repository.upsertCompatibleAccount(ctx, compatibleAccountInput{
		CompatibleImportItem: CompatibleImportItem{Token: oldToken, CookieHeader: "session=refreshable", RouteAffinity: route.ID},
		AccountID:            accountID, CookieJar: cookieJarFromHeader("session=refreshable"), Source: "cookie",
		CapturedAt: time.Now().UTC(), ExpiresAt: &expiresAt, Policy: normalizeClientPolicy(ClientPolicy{}),
	})
	if err != nil {
		t.Fatal(err)
	}

	creditsCalls, refreshCalls := 0, 0
	runtime.httpClientFactory = func(context.Context, string, time.Duration) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			header := make(http.Header)
			switch request.URL.String() {
			case adobeCreditsURL:
				creditsCalls++
				if creditsCalls == 1 {
					return &http.Response{StatusCode: http.StatusUnauthorized, Header: header, Body: io.NopCloser(strings.NewReader(`{"error_description":"Oauth token is not valid"}`))}, nil
				}
				if request.Header.Get("Authorization") != "Bearer "+newToken {
					t.Fatalf("second credits request did not use refreshed token")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(`{"total":{"quota":{"total":100,"used":25,"available":75}}}`))}, nil
			case Adobe2APIRefreshURL:
				refreshCalls++
				if !strings.Contains(request.Header.Get("Cookie"), "session=refreshable") {
					t.Fatalf("refresh cookie was not forwarded")
				}
				body, _ := json.Marshal(map[string]any{"access_token": newToken, "expires_in": 7200})
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}
		})}, nil
	}

	updated, err := runtime.RefreshAccountCredits(ctx, account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if creditsCalls != 2 || refreshCalls != 1 || updated.State != "ready" || updated.CreditsAvailable == nil || *updated.CreditsAvailable != 75 || updated.CreditsError != "" {
		t.Fatalf("updated=%#v credits_calls=%d refresh_calls=%d", updated, creditsCalls, refreshCalls)
	}
	session, err := runtime.repository.AccountSession(ctx, account.AccountID)
	if err != nil || session.AccessToken != newToken {
		t.Fatalf("session token was not updated: err=%v", err)
	}
}

func testPostgresRuntime(t *testing.T) (*Runtime, func()) {
	t.Helper()
	databaseURL := os.Getenv("IMAGE_POOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set IMAGE_POOL_TEST_DATABASE_URL to run Adobe PostgreSQL integration tests")
	}
	postgres, err := persistence.OpenPostgres(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cipher, err := NewCipher(key)
	if err != nil {
		postgres.Close()
		t.Fatal(err)
	}
	repository, err := NewRepository(postgres.DB(), cipher)
	if err != nil {
		postgres.Close()
		t.Fatal(err)
	}
	runtime := &Runtime{config: config.AdobeConfig{IdempotencyTTLHours: 1}, repository: repository}
	truncate := func() {
		_, _ = postgres.DB().Exec(context.Background(), "TRUNCATE adobe_idempotency_requests,adobe_accounts,adobe_routes CASCADE")
	}
	truncate()
	return runtime, func() { truncate(); postgres.Close() }
}

func TestPostgresIdempotencyLifecycle(t *testing.T) {
	runtime, cleanup := testPostgresRuntime(t)
	defer cleanup()
	ctx := context.Background()

	decision, err := runtime.BeginIdempotentRequest(ctx, "user|endpoint", "key", "hash-a")
	if err != nil || !decision.Execute {
		t.Fatalf("first decision=%#v err=%v", decision, err)
	}
	if _, err := runtime.BeginIdempotentRequest(ctx, "user|endpoint", "key", "hash-a"); !errors.Is(err, ErrRequestInProgress) {
		t.Fatalf("in-progress err=%v", err)
	}
	if _, err := runtime.BeginIdempotentRequest(ctx, "user|endpoint", "key", "hash-b"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	payload := map[string]any{"data": []any{map[string]any{"url": "https://example.com/result.png"}}}
	if err := runtime.CompleteIdempotentRequest(ctx, "user|endpoint", "key", 200, payload); err != nil {
		t.Fatal(err)
	}
	replay, err := runtime.BeginIdempotentRequest(ctx, "user|endpoint", "key", "hash-a")
	if err != nil || !replay.Replay || replay.StatusCode != 200 || len(replay.ResponseBody) == 0 {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
}

func TestPostgresAdobe2APIImportAndRoundRobinSelection(t *testing.T) {
	runtime, cleanup := testPostgresRuntime(t)
	defer cleanup()
	ctx := context.Background()
	enabled := true
	route, err := runtime.repository.CreateRoute(ctx, RouteInput{Name: "direct", ProxyURL: "direct://", Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(time.Hour).UTC()
	for _, accountID := range []string{"compat-account-1", "compat-account-2"} {
		token := jwtWithClaims(map[string]any{"user_id": accountID, "exp": float64(expiresAt.Unix())})
		account, err := runtime.repository.upsertCompatibleAccount(ctx, compatibleAccountInput{
			CompatibleImportItem: CompatibleImportItem{Token: token, CookieHeader: "session=secret", RouteAffinity: route.ID, Name: accountID},
			AccountID:            accountID, CookieJar: cookieJarFromHeader("session=secret"), Source: "cookie",
			CapturedAt: time.Now().UTC(), ExpiresAt: &expiresAt, Policy: normalizeClientPolicy(ClientPolicy{}),
		})
		if err != nil || account.State != "ready" || account.RouteAffinity != route.ID || account.Source != "cookie" || !account.Refreshable {
			t.Fatalf("account=%#v err=%v", account, err)
		}
	}

	first, err := runtime.repository.SelectAccountSession(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.repository.SelectAccountSession(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Account.AccountID == second.Account.AccountID || first.AccessToken == "" || second.AccessToken == "" {
		t.Fatalf("first=%#v second=%#v", first.Account, second.Account)
	}
	preferred, err := runtime.repository.SelectAccountSession(ctx, nil, first.Account.AccountID)
	if err != nil || preferred.Account.AccountID != first.Account.AccountID {
		t.Fatalf("preferred=%#v err=%v", preferred.Account, err)
	}
	if err := runtime.repository.RecordGenerationResult(ctx, first.Account.AccountID, &upstreamError{Kind: "auth_invalid", Message: "expired", Retryable: true}); err != nil {
		t.Fatal(err)
	}
	updated, err := runtime.repository.GetAccount(ctx, first.Account.AccountID)
	if err != nil || updated.State != "reauth_required" {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	if _, err := runtime.repository.SelectAccountSession(ctx, nil, first.Account.AccountID); !errors.Is(err, ErrNoAvailableAccount) {
		t.Fatalf("preferred unavailable err=%v", err)
	}
	replacement, err := runtime.repository.CreateRoute(ctx, RouteInput{Name: "replacement", ProxyURL: "direct://", Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.repository.RecordRouteHealth(ctx, replacement.ID, nil, 3, 300); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.repository.ReassignAccountsFromRoute(ctx, route.ID); err != nil {
		t.Fatal(err)
	}
	updated, err = runtime.repository.GetAccount(ctx, first.Account.AccountID)
	if err != nil || updated.State != "reauth_required" || updated.RouteAffinity != replacement.ID {
		t.Fatalf("reassigned=%#v err=%v", updated, err)
	}
	if err := runtime.repository.DeleteRoute(ctx, replacement.ID); !errors.Is(err, ErrRouteInUse) {
		t.Fatalf("delete assigned route err=%v", err)
	}
	if err := runtime.repository.DeleteRoute(ctx, route.ID); err != nil {
		t.Fatalf("delete unassigned route: %v", err)
	}

	ids, err := runtime.repository.CompatibleRefreshAccountIDs(ctx)
	if err != nil || len(ids) != 2 {
		t.Fatalf("ids=%#v err=%v", ids, err)
	}
}
