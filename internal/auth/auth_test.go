package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"imagepool/internal/persistence"
)

type blockingAuthStore struct {
	mu      sync.Mutex
	block   bool
	started chan struct{}
	release chan struct{}
	saved   keyFile
}

func (s *blockingAuthStore) Load(context.Context, string, any) error {
	return persistence.ErrNotFound
}

func (s *blockingAuthStore) Save(_ context.Context, _ string, value any) error {
	s.mu.Lock()
	block := s.block
	release := s.release
	s.mu.Unlock()
	if block {
		select {
		case s.started <- struct{}{}:
		default:
		}
		<-release
	}
	shaped, ok := value.(keyFile)
	if !ok {
		return nil
	}
	s.mu.Lock()
	keys := make([]keyRecord, len(shaped.Keys))
	for i := range shaped.Keys {
		keys[i] = cloneRecord(shaped.Keys[i])
	}
	s.saved = keyFile{Keys: keys}
	s.mu.Unlock()
	return nil
}

func (s *blockingAuthStore) Delete(context.Context, string) error { return nil }

func (s *blockingAuthStore) Health(context.Context) (persistence.Health, error) {
	return persistence.Health{}, nil
}

func (s *blockingAuthStore) Close() {}

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
	defer svc.Close()
	now := time.Now()
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
	if err := svc.Flush(); err != nil {
		t.Fatal(err)
	}

	reloaded := NewService([]string{"admin"}, path)
	defer reloaded.Close()
	persistedKeys := reloaded.ListUserKeys()
	if len(persistedKeys) != 1 || persistedKeys[0].Usage.Requests != 1 || persistedKeys[0].Usage.Images != 1 || persistedKeys[0].LastUsedAt == "" {
		t.Fatalf("persisted keys=%#v", persistedKeys)
	}
	_, ok = reloaded.Authenticate(raw)
	if !ok {
		t.Fatal("persisted user key did not authenticate")
	}
}

func TestConsumeRejectionReleasesLock(t *testing.T) {
	svc := NewService(nil, "")
	item, _, err := svc.CreateUserKey("Rejected client")
	if err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		DailyRequests:    1,
		DailyImages:      1,
		AllowedEndpoints: []string{"/v1/images/generations"},
		AllowedModels:    []string{"gpt-image-2"},
	}
	if _, ok, err := svc.UpdateUserKey(item.ID, KeyUpdate{Limits: &limits}); err != nil || !ok {
		t.Fatalf("set limits ok=%v err=%v", ok, err)
	}
	identity := Identity{ID: item.ID, Name: item.Name, Role: RoleUser}
	consume := func(endpoint, model string, requests, images int) error {
		result := make(chan error, 1)
		go func() {
			result <- svc.Consume(identity, endpoint, model, requests, images)
		}()
		select {
		case err := <-result:
			return err
		case <-time.After(250 * time.Millisecond):
			t.Fatal("Consume remained blocked after a rejected request")
			return nil
		}
	}
	expectStatus := func(err error, status int) {
		t.Helper()
		quota, ok := err.(*QuotaError)
		if !ok || quota.StatusCode != status {
			t.Fatalf("quota error=%#v, expected status=%d", err, status)
		}
	}

	expectStatus(consume("/v1/search", "gpt-image-2", 0, 0), http.StatusForbidden)
	expectStatus(consume("/v1/images/generations", "gpt-image-3", 0, 0), http.StatusForbidden)
	if err := consume("/v1/images/generations", "gpt-image-2", 1, 1); err != nil {
		t.Fatal(err)
	}
	expectStatus(consume("/v1/images/generations", "gpt-image-2", 1, 0), http.StatusTooManyRequests)
	expectStatus(consume("/v1/images/generations", "gpt-image-2", 0, 1), http.StatusTooManyRequests)

	disabled := false
	if _, ok, err := svc.UpdateUserKey(item.ID, KeyUpdate{Enabled: &disabled}); err != nil || !ok {
		t.Fatalf("disable ok=%v err=%v", ok, err)
	}
	expectStatus(consume("/v1/images/generations", "gpt-image-2", 0, 0), http.StatusUnauthorized)
	enabled := true
	if _, ok, err := svc.UpdateUserKey(item.ID, KeyUpdate{Enabled: &enabled}); err != nil || !ok {
		t.Fatalf("enable ok=%v err=%v", ok, err)
	}
	if err := consume("/v1/images/generations", "gpt-image-2", 0, 0); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeDoesNotBlockOnPersistence(t *testing.T) {
	state := &blockingAuthStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	svc := NewServiceWithPersistence(nil, state)
	released := false
	defer func() {
		if !released {
			close(state.release)
		}
		svc.Close()
	}()

	item, _, err := svc.CreateUserKey("Concurrent client")
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{ID: item.ID, Name: item.Name, Role: RoleUser}

	state.mu.Lock()
	state.block = true
	state.mu.Unlock()
	if err := svc.Consume(identity, "/v1/images/generations", "gpt-image-2", 1, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-state.started:
	case <-time.After(time.Second):
		t.Fatal("auth persistence worker did not start")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 50)
	started := time.Now()
	for index := 0; index < 50; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.Consume(identity, "/v1/images/generations", "gpt-image-2", 1, 0)
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("50 Consume calls blocked on persistence")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("50 Consume calls blocked on persistence for %s", elapsed)
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if keys := svc.ListUserKeys(); len(keys) != 1 || keys[0].Usage.Requests != 51 {
		t.Fatalf("in-memory usage=%#v", keys)
	}

	released = true
	close(state.release)
	svc.Close()
	state.mu.Lock()
	persisted := state.saved
	state.mu.Unlock()
	if len(persisted.Keys) != 1 || persisted.Keys[0].Usage.Requests != 51 {
		t.Fatalf("persisted auth keys=%#v", persisted)
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
