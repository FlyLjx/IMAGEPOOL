package searches

import (
	"context"
	"errors"
	"testing"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/openaiweb"
)

type fakeSearchBackend struct {
	calls int
	errs  []error
}

func (f *fakeSearchBackend) Search(ctx context.Context, account accounts.Account, req openaiweb.SearchRequest) (openaiweb.SearchResult, error) {
	f.calls++
	if len(f.errs) >= f.calls && f.errs[f.calls-1] != nil {
		return openaiweb.SearchResult{}, f.errs[f.calls-1]
	}
	return openaiweb.SearchResult{Answer: "answer", AccountEmail: account.Email, Model: req.Model}, nil
}

func TestSearchQueuesInvalidTokenForRecovery(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "old", AccessToken: "old", CreatedAt: 1}, {Email: "new", AccessToken: "new", CreatedAt: 2}}, "")
	backend := &fakeSearchBackend{errs: []error{errors.New("token_revoked")}}
	got, err := NewService(config.Default(), store, backend).Search(context.Background(), "query")
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 2 || got.Answer != "answer" {
		t.Fatalf("calls=%d got=%#v", backend.calls, got)
	}
	invalid, found := store.Get("new")
	if !found || invalid.Status != accounts.StatusCredentialInvalid {
		t.Fatalf("token not queued for recovery: %#v", store.List())
	}
}
