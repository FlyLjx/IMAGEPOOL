package registration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestWorkerCompletesMockedRegistrationProtocol(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1":
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{"userAgent": registrationUA, "cookies": []map[string]string{{"name": "cf_clearance", "value": "clearance"}}}})
		case "POST /v2/inbox/create":
			json.NewEncoder(w).Encode(map[string]string{"address": "new@example.test", "token": "mail-token"})
		case "GET /v2/inbox":
			if r.URL.Query().Get("token") != "mail-token" {
				t.Fatalf("mail token=%q", r.URL.Query().Get("token"))
			}
			json.NewEncoder(w).Encode(map[string]any{"emails": []map[string]string{{"subject": "Your verification code is 177010", "body": "Use 654321 to continue"}}})
		case "GET /api/accounts/authorize":
			if r.URL.Query().Get("code_challenge_method") != "S256" || r.URL.Query().Get("login_hint") != "new@example.test" {
				t.Fatalf("authorize query=%s", r.URL.RawQuery)
			}
			if cookie, err := r.Cookie("cf_clearance"); err != nil || cookie.Value != "clearance" {
				t.Fatalf("flaresolverr cookie=%q err=%v", cookie, err)
			}
			w.WriteHeader(http.StatusOK)
		case "POST /backend-api/sentinel/req":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if !strings.HasPrefix(payload["p"], "gAAAAAC") || payload["id"] == "" {
				t.Fatalf("bad sentinel payload=%#v", payload)
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "sentinel-token", "proofofwork": map[string]any{"required": true, "seed": "mock-seed", "difficulty": "ffffffff"}})
		case "POST /api/accounts/user/register":
			if !strings.Contains(r.Header.Get("OpenAI-Sentinel-Token"), "sentinel-token") || !strings.Contains(r.Header.Get("OpenAI-Sentinel-Token"), "gAAAAAB") || r.Header.Get("OAI-Device-Id") == "" {
				t.Fatalf("missing registration sentinel/device headers: %#v", r.Header)
			}
			w.WriteHeader(http.StatusOK)
		case "GET /api/accounts/email-otp/send":
			w.WriteHeader(http.StatusOK)
		case "POST /api/accounts/email-otp/validate":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["code"] != "654321" {
				t.Fatalf("otp=%q", payload["code"])
			}
			w.WriteHeader(http.StatusOK)
		case "POST /api/accounts/create_account":
			if !strings.Contains(r.Header.Get("OpenAI-Sentinel-Token"), "sentinel-token") || !strings.Contains(r.Header.Get("OpenAI-Sentinel-Token"), "gAAAAAB") {
				t.Fatal("missing create sentinel header")
			}
			json.NewEncoder(w).Encode(map[string]string{"continue_url": serverURL(r) + "/auth/callback?code=callback-code"})
		case "POST /api/accounts/oauth/token":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["code"] != "callback-code" || payload["code_verifier"] == "" {
				t.Fatalf("oauth payload=%#v", payload)
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "access", "refresh_token": "refresh", "id_token": "id"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	defer server.CloseClientConnections()

	worker := NewWorker(WorkerOptions{AuthURL: server.URL, PlatformURL: server.URL, SentinelURL: server.URL, HTTPClient: server.Client()})
	settings := Default()
	settings.FlareSolverr.Enabled = true
	settings.FlareSolverr.URL = server.URL
	settings.Mail.Providers = []map[string]any{{"type": "tempmail_lol", "enabled": true, "api_base": server.URL + "/v2"}}
	account, err := worker(context.Background(), settings, 1)
	if err != nil {
		t.Fatal(err)
	}
	if account.Email != "new@example.test" || account.AccessToken != "access" || account.RefreshToken != "refresh" || account.Password == "" || account.DeviceID == "" {
		t.Fatalf("account=%#v", account)
	}
	want := []string{"POST /v2/inbox/create", "POST /v1", "GET /api/accounts/authorize", "POST /backend-api/sentinel/req", "POST /api/accounts/user/register", "GET /api/accounts/email-otp/send", "GET /v2/inbox", "POST /api/accounts/email-otp/validate", "POST /backend-api/sentinel/req", "POST /api/accounts/create_account", "POST /api/accounts/oauth/token"}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("calls=%#v\nwant=%#v", seen, want)
	}
}

func TestTempMailRequiresEnabledProvider(t *testing.T) {
	_, err := newTempMailProvider(nil).Create(context.Background(), Mail{Providers: []map[string]any{{"type": "tempmail_lol", "enabled": false}}})
	if err == nil || !strings.Contains(err.Error(), "no enabled") {
		t.Fatalf("err=%v", err)
	}
}

func TestEnabledProvidersRotateInConfiguredOrder(t *testing.T) {
	providers := []map[string]any{{"type": "tempmail_lol", "enabled": true, "api_base": "one"}, {"type": "tempmail_lol", "enabled": false, "api_base": "disabled"}, {"type": "tempmail_lol", "enable": true, "api_base": "two"}}
	for index, want := range []string{"one", "two", "one", "two"} {
		provider, err := enabledProviderAt(providers, uint64(index))
		if err != nil || provider["api_base"] != want {
			t.Fatalf("index=%d provider=%#v err=%v", index, provider, err)
		}
	}
}

func serverURL(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}
