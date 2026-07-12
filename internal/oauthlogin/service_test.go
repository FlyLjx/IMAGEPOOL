package oauthlogin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestStartAndFinishExchangesPKCECode(t *testing.T) {
	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/oauth/token" || r.Method != http.MethodPost {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","id_token":"id"}`))
	}))
	defer server.Close()
	svc := NewWithClient(server.URL, server.Client())
	started, err := svc.Start("person@example.test")
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(started["authorize_url"])
	if err != nil {
		t.Fatal(err)
	}
	if authorize.Query().Get("login_hint") != "person@example.test" || authorize.Query().Get("code_challenge") == "" {
		t.Fatalf("authorize=%s", authorize)
	}
	callback := "https://platform.openai.com/auth/callback?code=one-time&state=" + url.QueryEscape(authorize.Query().Get("state"))
	tokens, err := svc.Finish(started["session_id"], callback)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access" || received["code"] != "one-time" || received["code_verifier"] == "" {
		t.Fatalf("tokens=%#v body=%#v", tokens, received)
	}
	if _, err := svc.Finish(started["session_id"], callback); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected consumed session error, got %v", err)
	}
}

func TestFinishRejectsStateMismatchAndExpiry(t *testing.T) {
	svc := NewWithClient("https://example.test", http.DefaultClient)
	started, err := svc.Start("")
	if err != nil {
		t.Fatal(err)
	}
	wrongState := started["session_id"] + ".wrong"
	if _, err := svc.Finish(started["session_id"], "https://platform.openai.com/auth/callback?code=x&state="+wrongState); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("expected state error, got %v", err)
	}
	svc.mu.Lock()
	entry := svc.sessions[started["session_id"]]
	entry.Created = time.Now().Add(-sessionTTL - time.Second)
	svc.sessions[started["session_id"]] = entry
	svc.mu.Unlock()
	if _, err := svc.Finish(started["session_id"], "x"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expiry error, got %v", err)
	}
}

func TestRefreshTokenUsesRefreshGrant(t *testing.T) {
	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/oauth/token" || r.Method != http.MethodPost {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"rotated","id_token":"id"}`))
	}))
	defer server.Close()
	svc := NewWithClient(server.URL, server.Client())
	access, refresh, id, err := svc.RefreshToken(context.Background(), "saved-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if access != "access" || refresh != "rotated" || id != "id" || received["grant_type"] != "refresh_token" || received["refresh_token"] != "saved-refresh" {
		t.Fatalf("tokens=%q/%q/%q payload=%#v", access, refresh, id, received)
	}
}
