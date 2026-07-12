package texts

import (
	"context"
	"errors"
	"testing"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/openaiweb"
)

type fakeTextBackend struct {
	calls int
	errs  []error
}

func (f *fakeTextBackend) GenerateText(ctx context.Context, account accounts.Account, req openaiweb.ChatRequest) (openaiweb.ChatResult, error) {
	f.calls++
	if len(f.errs) >= f.calls && f.errs[f.calls-1] != nil {
		return openaiweb.ChatResult{}, f.errs[f.calls-1]
	}
	return openaiweb.ChatResult{Text: "ok", Model: req.Model, AccountEmail: account.Email, ConversationID: "conv"}, nil
}
func (f *fakeTextBackend) StreamText(ctx context.Context, account accounts.Account, req openaiweb.ChatRequest, emit func(openaiweb.ChatDelta) error) (string, error) {
	f.calls++
	if len(f.errs) >= f.calls && f.errs[f.calls-1] != nil {
		return "", f.errs[f.calls-1]
	}
	_ = emit(openaiweb.ChatDelta{Delta: "o", ConversationID: "conv"})
	_ = emit(openaiweb.ChatDelta{Delta: "k", ConversationID: "conv"})
	return "conv", nil
}

func TestGenerateRemovesInvalidToken(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "old", AccessToken: "old", CreatedAt: 1}, {Email: "new", AccessToken: "new", CreatedAt: 2}}, "")
	backend := &fakeTextBackend{errs: []error{errors.New("token invalidated")}}
	res, err := NewService(config.Default(), store, backend).Generate(context.Background(), openaiweb.ChatRequest{Model: "auto", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 2 || res.Text != "ok" {
		t.Fatalf("calls=%d res=%#v", backend.calls, res)
	}
	if _, found := store.Get("new"); found {
		t.Fatalf("token was not removed: %#v", store.List())
	}
}

func TestStreamCollectsDeltas(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "a", AccessToken: "a", CreatedAt: 1}}, "")
	backend := &fakeTextBackend{}
	var streamed string
	res, err := NewService(config.Default(), store, backend).Stream(context.Background(), openaiweb.ChatRequest{Model: "auto", Prompt: "hi"}, func(d openaiweb.ChatDelta) error { streamed += d.Delta; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if streamed != "ok" || res.Text != "ok" {
		t.Fatalf("streamed=%q res=%#v", streamed, res)
	}
}
