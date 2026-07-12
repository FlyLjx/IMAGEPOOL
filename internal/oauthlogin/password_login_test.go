package oauthlogin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type staticEmailOTPReader struct {
	mu      sync.Mutex
	address string
	code    string
	calls   int
}

func (r *staticEmailOTPReader) ReadVerificationCode(_ context.Context, address string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.address = address
	return r.code, nil
}

func TestReLoginCompletesEmailOTPPasswordFlow(t *testing.T) {
	var mu sync.Mutex
	steps := make([]string, 0, 8)
	appendStep := func(value string) {
		mu.Lock()
		steps = append(steps, value)
		mu.Unlock()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendStep(r.Method + " " + r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/accounts/authorize":
			if r.URL.Query().Get("screen_hint") != "login" || r.URL.Query().Get("login_hint") != "person@example.test" || r.URL.Query().Get("code_challenge") == "" {
				t.Fatalf("authorize query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"page":{"type":"password"}}`))
		case "POST /backend-api/sentinel/req":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["flow"] != "password_verify" || !strings.HasPrefix(payload["p"], "gAAAAAC") || payload["id"] == "" {
				t.Fatalf("sentinel payload=%#v", payload)
			}
			_, _ = w.Write([]byte(`{"token":"sentinel-token","proofofwork":{"required":false}}`))
		case "POST /api/accounts/password/verify":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["password"] != "saved-password" || !strings.Contains(r.Header.Get("OpenAI-Sentinel-Token"), "sentinel-token") || r.Header.Get("OAI-Device-Id") == "" {
				t.Fatalf("password payload=%#v headers=%#v", payload, r.Header)
			}
			_, _ = w.Write([]byte(`{"page":{"type":"email_otp_verification"}}`))
		case "GET /api/accounts/email-otp/send":
			w.WriteHeader(http.StatusOK)
		case "POST /api/accounts/email-otp/validate":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["code"] != "654321" {
				t.Fatalf("otp=%q", payload["code"])
			}
			_, _ = fmt.Fprintf(w, `{"continue_url":"http://%s/auth/callback?code=authorization-code"}`, r.Host)
		case "POST /api/accounts/oauth/token":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["grant_type"] != "authorization_code" || payload["code"] != "authorization-code" || payload["code_verifier"] == "" {
				t.Fatalf("token payload=%#v", payload)
			}
			_, _ = w.Write([]byte(`{"access_token":"fresh-access","refresh_token":"fresh-refresh","id_token":"fresh-id"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	reader := &staticEmailOTPReader{code: "654321"}
	service := NewWithClientAndSentinel(server.URL, server.URL, server.Client())
	service.SetEmailOTPReader(reader)
	access, refresh, id, err := service.ReLogin(context.Background(), "person@example.test", "saved-password")
	if err != nil {
		t.Fatal(err)
	}
	if access != "fresh-access" || refresh != "fresh-refresh" || id != "fresh-id" {
		t.Fatalf("tokens=%q/%q/%q", access, refresh, id)
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.calls != 1 || reader.address != "person@example.test" {
		t.Fatalf("reader=%#v", reader)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/accounts/authorize",
		"POST /backend-api/sentinel/req",
		"POST /api/accounts/password/verify",
		"GET /api/accounts/email-otp/send",
		"POST /api/accounts/email-otp/validate",
		"POST /api/accounts/oauth/token",
	}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("steps=%#v want=%#v", steps, want)
	}
}
