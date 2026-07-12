package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthAllowedHeaders(t *testing.T) {
	a := New([]string{"secret"})
	r := httptest.NewRequest("GET", "/", nil)
	if a.Allowed(r) {
		t.Fatal("allowed without key")
	}
	r.Header.Set("Authorization", "Bearer secret")
	if !a.Allowed(r) {
		t.Fatal("bearer not accepted")
	}
	r.Header.Del("Authorization")
	r.Header.Set("x-api-key", "secret")
	if !a.Allowed(r) {
		t.Fatal("x-api-key not accepted")
	}
}

func TestUserKeyQuotaAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-keys.json")
	svc := NewService([]string{"admin"}, path)
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	created, raw, err := svc.CreateUserKey("Client")
	if err != nil || raw == "" || created.Role != RoleUser {
		t.Fatalf("created=%#v raw=%q err=%v", created, raw, err)
	}
	limit := Limits{DailyRequests: 2, DailyImages: 1, AllowedEndpoints: []string{"/v1/images/generations"}, AllowedModels: []string{"gpt-image-2"}}
	updated, ok, err := svc.UpdateUserKey(created.ID, KeyUpdate{Limits: &limit})
	if err != nil || !ok || updated.Limits.DailyImages != 1 {
		t.Fatalf("updated=%#v ok=%v err=%v", updated, ok, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	identity, ok := svc.AuthenticateRequest(req)
	if !ok || identity.Role != RoleUser {
		t.Fatalf("identity=%#v ok=%v", identity, ok)
	}
	if err := svc.Consume(identity, "/v1/images/generations", "gpt-image-2", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(identity, "/v1/images/generations", "gpt-image-2", 1, 1); err == nil {
		t.Fatal("expected image quota error")
	} else if quota, ok := err.(*QuotaError); !ok || quota.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("unexpected error %#v", err)
	}
	if err := svc.Consume(identity, "/v1/search", "gpt-image-2", 1, 0); err == nil {
		t.Fatal("expected endpoint permission error")
	}

	reloaded := NewService([]string{"admin"}, path)
	if len(reloaded.ListUserKeys()) != 1 {
		t.Fatalf("persisted keys=%#v", reloaded.ListUserKeys())
	}
	_, ok = reloaded.Authenticate(raw)
	if !ok {
		t.Fatal("persisted user key did not authenticate")
	}
}

func TestUserKeyDisableAndDelete(t *testing.T) {
	svc := NewService([]string{"admin"}, "")
	item, raw, err := svc.CreateUserKey("Client")
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, ok, err := svc.UpdateUserKey(item.ID, KeyUpdate{Enabled: &disabled}); err != nil || !ok {
		t.Fatalf("disable ok=%v err=%v", ok, err)
	}
	if _, ok := svc.Authenticate(raw); ok {
		t.Fatal("disabled key authenticated")
	}
	removed, err := svc.DeleteUserKey(item.ID)
	if err != nil || !removed || len(svc.ListUserKeys()) != 0 {
		t.Fatalf("removed=%v err=%v keys=%#v", removed, err, svc.ListUserKeys())
	}
}
