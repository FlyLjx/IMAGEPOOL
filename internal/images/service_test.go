package images

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/openaiweb"
	"imagepool/internal/storage"
)

type fakeBackend struct {
	mu              sync.Mutex
	calls           int
	errs            []error
	modelErrs       map[string]error
	modelTokens     []string
	readinessErrs   map[string]error
	readinessTokens []string
	readinessFn     func(string) error
}

type accountInfoRefreshBackend struct {
	*fakeBackend
	info openaiweb.AccountInfo
}

func (b *accountInfoRefreshBackend) GetAccountInfo(context.Context, string) (openaiweb.AccountInfo, error) {
	return b.info, nil
}

type cacheBackend struct {
	*fakeBackend
	downloadedAccount accounts.Account
}

type serialImageBackend struct {
	*fakeBackend
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	active  int
	max     int
}

func (b *serialImageBackend) GenerateImage(ctx context.Context, account accounts.Account, req openaiweb.ImageRequest) (openaiweb.ImageResult, error) {
	b.mu.Lock()
	b.calls++
	b.active++
	if b.active > b.max {
		b.max = b.active
	}
	b.mu.Unlock()
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
		return openaiweb.ImageResult{}, ctx.Err()
	}
	b.mu.Lock()
	b.active--
	b.mu.Unlock()
	return openaiweb.ImageResult{URLs: []string{"https://example.com/image.png"}, AccountEmail: account.Email, BackendModel: "gpt-5-5", ConversationID: "conv"}, nil
}

func (b *serialImageBackend) stats() (calls, max int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, b.max
}

func (c *cacheBackend) DownloadImageFor(ctx context.Context, account accounts.Account, imageURL string) ([]byte, error) {
	c.downloadedAccount = account
	return []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00}, nil
}

func (f *fakeBackend) GenerateImage(ctx context.Context, account accounts.Account, req openaiweb.ImageRequest) (openaiweb.ImageResult, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	var err error
	if len(f.errs) >= call {
		err = f.errs[call-1]
	}
	f.mu.Unlock()
	if err != nil {
		return openaiweb.ImageResult{}, err
	}
	return openaiweb.ImageResult{URLs: []string{"https://example.com/image.png"}, AccountEmail: account.Email, BackendModel: "gpt-5-5", ConversationID: "conv"}, nil
}
func (f *fakeBackend) ListModels(ctx context.Context, token string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modelTokens = append(f.modelTokens, token)
	if err := f.modelErrs[token]; err != nil {
		return nil, err
	}
	return []string{"gpt-5-5"}, nil
}
func (f *fakeBackend) CheckImageReady(ctx context.Context, token string) error {
	f.mu.Lock()
	f.readinessTokens = append(f.readinessTokens, token)
	readinessFn := f.readinessFn
	err := f.readinessErrs[token]
	f.mu.Unlock()
	if readinessFn != nil {
		return readinessFn(token)
	}
	return err
}
func (f *fakeBackend) Search(ctx context.Context, account accounts.Account, req openaiweb.SearchRequest) (openaiweb.SearchResult, error) {
	return openaiweb.SearchResult{Answer: "ok"}, nil
}

func TestCheckAccountRefreshSkipsImageSpecificHandshake(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "a@example.com", AccessToken: "token"}}, "")
	backend := &accountInfoRefreshBackend{
		fakeBackend: &fakeBackend{},
		info:        openaiweb.AccountInfo{Email: "a@example.com", Type: "free", Quota: 5},
	}
	result, err := NewService(config.Default(), store, backend).CheckAccountLight(context.Background(), "token")
	if err != nil || result.Email != "a@example.com" || result.Quota != 5 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(backend.readinessTokens) != 0 || len(backend.modelTokens) != 0 {
		t.Fatalf("refresh should not run image handshake or model listing: readiness=%#v models=%#v", backend.readinessTokens, backend.modelTokens)
	}
}

func TestGenerateRemovesInvalidTokenAndRetriesNextAccount(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "old", AccessToken: "old", CreatedAt: 1}, {Email: "new", AccessToken: "new", CreatedAt: 2}}, "")
	fb := &fakeBackend{errs: []error{errors.New("token_revoked")}}
	resp, err := NewService(config.Default(), store, fb).Generate(context.Background(), Request{Prompt: "draw", Model: "gpt-image-2"})
	if err != nil {
		t.Fatal(err)
	}
	if fb.calls != 2 {
		t.Fatalf("calls=%d", fb.calls)
	}
	if resp.AccountEmail != "old" {
		t.Fatalf("expected old after removing new, got %s", resp.AccountEmail)
	}
	if _, found := store.Get("new"); found {
		t.Fatalf("invalid account was not removed: %#v", store.List())
	}
}

func TestGenerateRemovesGeneric401Account(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "old", AccessToken: "old", CreatedAt: 1}, {Email: "new", AccessToken: "new", CreatedAt: 2}}, "")
	fb := &fakeBackend{errs: []error{errors.New("upstream /backend-api/me status=401 body=unauthorized")}}
	response, err := NewService(config.Default(), store, fb).Generate(context.Background(), Request{Prompt: "draw"})
	if err != nil || response.AccountEmail != "old" || fb.calls != 2 {
		t.Fatalf("response=%#v err=%v calls=%d", response, err, fb.calls)
	}
	if _, found := store.Get("new"); found {
		t.Fatalf("generic 401 account was not removed: %#v", store.List())
	}
}

func TestGenerateAppliesSingleTaskDeadlineAcrossRetries(t *testing.T) {
	cfg := config.Default()
	cfg.ImageTaskTimeoutSecs = 0.02
	store := accounts.NewStore([]accounts.Account{{Email: "a@example", AccessToken: "token"}}, "")
	backend := &serialImageBackend{fakeBackend: &fakeBackend{}, started: make(chan struct{}, 1), release: make(chan struct{})}
	_, err := NewService(cfg, store, backend).Generate(context.Background(), Request{Prompt: "draw"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestGenerateSkipsDuplicateReadinessPrecheck(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "old", AccessToken: "old", CreatedAt: 1}, {Email: "new", AccessToken: "new", CreatedAt: 2}}, "")
	backend := &fakeBackend{readinessErrs: map[string]error{"new": errors.New("token invalidated (/backend-api/sentinel/chat-requirements/prepare)")}}
	response, err := NewService(config.Default(), store, backend).Generate(context.Background(), Request{Prompt: "draw", Model: "gpt-image-2"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.calls != 1 || response.AccountEmail != "new" {
		t.Fatalf("calls=%d response=%#v", backend.calls, response)
	}
	if _, found := store.Get("new"); !found {
		t.Fatalf("dispatch removed an account without a generation failure: %#v", store.List())
	}
	if len(backend.readinessTokens) != 0 {
		t.Fatalf("normal dispatch repeated readiness checks: %#v", backend.readinessTokens)
	}
}

func TestManualCheckStillRunsFullReadinessValidation(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "user@example.test", AccessToken: "token"}}, "")
	backend := &fakeBackend{readinessErrs: map[string]error{"token": errors.New("token invalidated")}}
	_, err := NewService(config.Default(), store, backend).CheckAccount(context.Background(), "token")
	if err == nil || len(backend.readinessTokens) != 1 || backend.readinessTokens[0] != "token" {
		t.Fatalf("err=%v readiness=%#v", err, backend.readinessTokens)
	}
}

func TestGenerateQueuesUntilAnOccupiedAccountIsReleased(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "one", AccessToken: "one", CreatedAt: 1}}, "")
	backend := &serialImageBackend{
		fakeBackend: &fakeBackend{},
		started:     make(chan struct{}, 2),
		release:     make(chan struct{}),
	}
	service := NewService(config.Default(), store, backend)
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Generate(context.Background(), Request{Prompt: "first"})
		firstDone <- err
	}()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}

	waiting := make(chan struct{}, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := service.Generate(context.Background(), Request{Prompt: "second", Progress: func(event openaiweb.ProgressEvent) {
			if event.Progress == "waiting_account" {
				select {
				case waiting <- struct{}{}:
				default:
				}
			}
		}})
		secondDone <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("second request did not enter the account queue")
	}
	if calls, max := backend.stats(); calls != 1 || max != 1 {
		t.Fatalf("calls=%d maxActive=%d", calls, max)
	}

	close(backend.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued second request did not finish")
	}
	if calls, max := backend.stats(); calls != 2 || max != 1 {
		t.Fatalf("calls=%d maxActive=%d", calls, max)
	}
}

func TestGenerateLimitsNoQuotaAccountAndRetries(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "a", AccessToken: "a", CreatedAt: 1}, {Email: "b", AccessToken: "b", CreatedAt: 2}}, "")
	fb := &fakeBackend{errs: []error{errors.New("no available free image quota (tried 20 tokens)")}}
	_, err := NewService(config.Default(), store, fb).Generate(context.Background(), Request{Prompt: "draw"})
	if err != nil {
		t.Fatal(err)
	}
	items := store.List()
	if len(items) != 2 || items[1].Status != "限流" {
		t.Fatalf("no quota account was not limited: %#v", items)
	}
}

func TestGenerateUpdatesKnownImageQuotaAfterSuccess(t *testing.T) {
	refreshedAt := time.Now().UTC().Format(time.RFC3339)
	store := accounts.NewStore([]accounts.Account{{
		AccessToken: "token",
		Status:      "正常",
		Quota:       25,
		Extra:       map[string]any{"last_account_refresh_at": refreshedAt},
	}}, "")
	_, err := NewService(config.Default(), store, &fakeBackend{}).Generate(context.Background(), Request{Prompt: "draw"})
	if err != nil {
		t.Fatal(err)
	}
	account, found := store.Get("token")
	if !found || account.ImageOK != 1 || account.Quota != 24 {
		t.Fatalf("account=%#v found=%v", account, found)
	}
}

func TestGenerateInteractiveChallengePreservesAccounts(t *testing.T) {
	store := accounts.NewStore([]accounts.Account{{Email: "old", AccessToken: "old", CreatedAt: 1}, {Email: "new", AccessToken: "new", CreatedAt: 2}}, "")
	fb := &fakeBackend{errs: []error{errors.New("chat requirements requires turnstile token")}}
	_, err := NewService(config.Default(), store, fb).Generate(context.Background(), Request{Prompt: "draw"})
	if err == nil || !openaiweb.IsInteractiveChallengeError(err) {
		t.Fatalf("err=%v", err)
	}
	if fb.calls != 1 {
		t.Fatalf("calls=%d, want one request", fb.calls)
	}
	items := store.List()
	if len(items) != 2 || items[1].ImageFailures != 0 {
		t.Fatalf("challenge must preserve accounts: %#v", items)
	}
}

func TestListModelsFallback(t *testing.T) {
	models, err := NewService(config.Default(), accounts.NewStore(nil, ""), &fakeBackend{}).ListModels(context.Background())
	if err != nil || len(models) == 0 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
}

func TestGenerateCachesRemoteImageLocally(t *testing.T) {
	cfg := config.Default()
	cfg.ImageOutputDir = t.TempDir()
	store := accounts.NewStore([]accounts.Account{{AccessToken: "token", CreatedAt: 1}}, "")
	backend := &cacheBackend{fakeBackend: &fakeBackend{}}
	service := NewService(cfg, store, backend, storage.NewService(cfg))
	response, err := service.Generate(context.Background(), Request{Prompt: "draw", OutputBaseURL: "https://pool.example"})
	if err != nil || len(response.Data) != 1 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	parsed, err := url.Parse(response.Data[0].URL)
	if err != nil || parsed.Host != "pool.example" {
		t.Fatalf("url=%q err=%v", response.Data[0].URL, err)
	}
	items, err := storage.NewService(cfg).List("https://pool.example", "", "")
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if backend.downloadedAccount.AccessToken != "token" {
		t.Fatalf("download account=%#v", backend.downloadedAccount)
	}
}

func TestFiftyConcurrentTasksDispatchWithoutReadinessGate(t *testing.T) {
	cfg := config.Default()
	items := make([]accounts.Account, 0, 50)
	for i := 0; i < 50; i++ {
		token := fmt.Sprintf("token-%02d", i)
		items = append(items, accounts.Account{Email: token + "@example.test", AccessToken: token, Status: "正常"})
	}
	release := make(chan struct{})
	backend := &serialImageBackend{
		fakeBackend: &fakeBackend{},
		started:     make(chan struct{}, 50),
		release:     release,
	}
	service := NewService(cfg, accounts.NewStore(items, ""), backend)
	start := make(chan struct{})
	done := make(chan error, 50)
	for _, account := range items {
		token := account.AccessToken
		go func() {
			<-start
			_, err := service.GenerateWithAccount(context.Background(), token, Request{Prompt: "draw"})
			done <- err
		}()
	}
	close(start)
	for range items {
		select {
		case <-backend.started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("concurrent generation was blocked before the backend call")
		}
	}
	if calls, maxActive := backend.stats(); calls != len(items) || maxActive != len(items) {
		close(release)
		t.Fatalf("calls=%d max_active=%d", calls, maxActive)
	}
	if len(backend.readinessTokens) != 0 {
		close(release)
		t.Fatalf("readiness checks=%#v", backend.readinessTokens)
	}
	close(release)
	for i := 0; i < len(items); i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent image task did not complete")
		}
	}
}
