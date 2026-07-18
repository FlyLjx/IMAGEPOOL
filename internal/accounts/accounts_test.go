package accounts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepool/internal/persistence"
)

type blockingAccountStore struct {
	mu      sync.Mutex
	block   bool
	started chan struct{}
	release chan struct{}
	saved   fileShape
}

func (s *blockingAccountStore) Load(context.Context, string, any) error {
	return persistence.ErrNotFound
}

func (s *blockingAccountStore) Save(_ context.Context, _ string, value any) error {
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
	shaped, ok := value.(fileShape)
	if !ok {
		return nil
	}
	accounts := make([]Account, len(shaped.Accounts))
	for index := range shaped.Accounts {
		accounts[index] = cloneAccount(shaped.Accounts[index])
	}
	s.mu.Lock()
	s.saved = fileShape{Accounts: accounts, CredentialRecoveryLogs: append([]CredentialRecoveryLog(nil), shaped.CredentialRecoveryLogs...)}
	s.mu.Unlock()
	return nil
}

func (s *blockingAccountStore) Delete(context.Context, string) error { return nil }

func (s *blockingAccountStore) Health(context.Context) (persistence.Health, error) {
	return persistence.Health{}, nil
}

func (s *blockingAccountStore) Close() {}

func TestSelectNewestAccountFirst(t *testing.T) {
	store := NewStore([]Account{{Email: "old@example.com", AccessToken: "old", CreatedAt: 10}, {Email: "new@example.com", AccessToken: "new", CreatedAt: 20}}, "")
	got, err := store.SelectForImage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new" {
		t.Fatalf("selected %q", got.AccessToken)
	}
	got, err = store.SelectForImage(map[string]bool{"new": true})
	if err != nil || got.AccessToken != "old" {
		t.Fatalf("exclude got=%#v err=%v", got, err)
	}
}

func TestSelectRecentlyImportedAccountsFirst(t *testing.T) {
	store := NewStore([]Account{{Email: "legacy@example.com", AccessToken: "legacy", CreatedAt: 9999}}, "")
	store.now = func() time.Time { return time.Unix(100, 0) }
	if _, _, err := store.AddWithResult([]Account{{Email: "fresh@example.com", AccessToken: "fresh", CreatedAt: 1}}); err != nil {
		t.Fatal(err)
	}

	fresh, found := store.Get("fresh")
	if !found || fresh.ImportedAt != 100 || fresh.CreatedAt != 1 {
		t.Fatalf("fresh=%#v found=%v", fresh, found)
	}
	selected, err := store.SelectForImage(nil)
	if err != nil || selected.AccessToken != "fresh" {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}
}

func TestSelectUnusedAccountsFirstWithinNewestImportBatch(t *testing.T) {
	store := NewStore(nil, "")
	store.now = func() time.Time { return time.Unix(100, 0) }
	if _, _, err := store.AddWithResult([]Account{{AccessToken: "first"}, {AccessToken: "second"}}); err != nil {
		t.Fatal(err)
	}
	first, err := store.SelectForImage(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkImageSuccess(first.AccessToken); err != nil {
		t.Fatal(err)
	}
	second, err := store.SelectForImage(nil)
	if err != nil || second.AccessToken == first.AccessToken {
		t.Fatalf("first=%#v second=%#v err=%v", first, second, err)
	}
}

func TestSelectForImageRetainsPriorityTiebreakers(t *testing.T) {
	tests := []struct {
		name     string
		accounts []Account
		prepare  func(*Store)
		want     string
	}{
		{
			name: "imported account outranks legacy account",
			accounts: []Account{
				{AccessToken: "legacy", CreatedAt: 999},
				{AccessToken: "imported", ImportedAt: 1, CreatedAt: 1},
			},
			want: "imported",
		},
		{
			name: "newer import outranks older import",
			accounts: []Account{
				{AccessToken: "older", ImportedAt: 10, CreatedAt: 100},
				{AccessToken: "newer", ImportedAt: 20, CreatedAt: 1},
			},
			want: "newer",
		},
		{
			name: "less recently used account wins within import batch",
			accounts: []Account{
				{AccessToken: "used", ImportedAt: 20, LastUsedAt: 100, CreatedAt: 100},
				{AccessToken: "unused", ImportedAt: 20, LastUsedAt: 1, CreatedAt: 1},
			},
			want: "unused",
		},
		{
			name: "legacy accounts use created time instead of last used time",
			accounts: []Account{
				{AccessToken: "older", LastUsedAt: 999, CreatedAt: 1},
				{AccessToken: "newer", LastUsedAt: 0, CreatedAt: 2},
			},
			want: "newer",
		},
		{
			name: "created time breaks an imported batch tie",
			accounts: []Account{
				{AccessToken: "older", ImportedAt: 20, LastUsedAt: 0, CreatedAt: 1},
				{AccessToken: "newer", ImportedAt: 20, LastUsedAt: 0, CreatedAt: 2},
			},
			want: "newer",
		},
		{
			name: "later loaded account breaks a full timestamp tie",
			accounts: []Account{
				{AccessToken: "first", CreatedAt: 1},
				{AccessToken: "second", CreatedAt: 1},
			},
			want: "second",
		},
		{
			name: "email breaks a tie when loaded order is shared",
			accounts: []Account{
				{AccessToken: "first", Email: "a@example.com", CreatedAt: 1},
				{AccessToken: "second", Email: "z@example.com", CreatedAt: 1},
			},
			prepare: func(store *Store) {
				store.mu.Lock()
				store.accounts[0].loadedOrder = 7
				store.accounts[1].loadedOrder = 7
				store.mu.Unlock()
			},
			want: "second",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(test.accounts, "")
			if test.prepare != nil {
				test.prepare(store)
			}
			got, err := store.SelectForImage(nil)
			if err != nil || got.AccessToken != test.want {
				t.Fatalf("selected=%#v err=%v, want %q", got, err, test.want)
			}
		})
	}
}

func TestSelectForImageLargePoolMatchesLegacyOrder(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	items := make([]Account, 0, 4096)
	for index := 0; index < cap(items); index++ {
		account := Account{
			AccessToken: fmt.Sprintf("token-%04d", index),
			Email:       fmt.Sprintf("account-%04d@example.com", 4096-index),
			CreatedAt:   int64((index * 17) % 97),
			LastUsedAt:  int64((index * 31) % 67),
			Extra:       map[string]any{},
		}
		if index%5 != 0 {
			account.ImportedAt = int64(1000 + index%11)
		}
		if index%37 == 0 {
			account.Disabled = true
		}
		if index%41 == 0 {
			account.Status = "限流"
		}
		if index%43 == 0 {
			account.Extra[imageCooldownUntilKey] = now.Add(time.Minute).UTC().Format(time.RFC3339Nano)
		}
		items = append(items, account)
	}
	store := NewStore(items, "")
	store.now = func() time.Time { return now }
	exclude := map[string]bool{"token-0011": true, "token-1011": true, "token-2011": true}

	store.mu.RLock()
	want, ok := legacySelectForImageForTest(store.accounts, exclude, now)
	store.mu.RUnlock()
	if !ok {
		t.Fatal("legacy selection found no account")
	}
	got, err := store.SelectForImage(exclude)
	if err != nil || got.AccessToken != want.AccessToken {
		t.Fatalf("selected=%#v err=%v, legacy=%#v", got, err, want)
	}
}

// legacySelectForImageForTest retains the pre-optimization implementation so
// the large-pool test guards every ordering tier against future regressions.
func legacySelectForImageForTest(accounts []Account, exclude map[string]bool, now time.Time) (Account, bool) {
	candidates := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if !usable(account) || isImageCooling(account, now) {
			continue
		}
		if exclude != nil && exclude[account.AccessToken] {
			continue
		}
		candidates = append(candidates, account)
	}
	if len(candidates) == 0 {
		return Account{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftImported := candidates[i].ImportedAt > 0
		rightImported := candidates[j].ImportedAt > 0
		if leftImported != rightImported {
			return leftImported
		}
		if leftImported && candidates[i].ImportedAt != candidates[j].ImportedAt {
			return candidates[i].ImportedAt > candidates[j].ImportedAt
		}
		if leftImported && candidates[i].LastUsedAt != candidates[j].LastUsedAt {
			return candidates[i].LastUsedAt < candidates[j].LastUsedAt
		}
		if candidates[i].CreatedAt != candidates[j].CreatedAt {
			return candidates[i].CreatedAt > candidates[j].CreatedAt
		}
		if candidates[i].loadedOrder != candidates[j].loadedOrder {
			return candidates[i].loadedOrder > candidates[j].loadedOrder
		}
		return candidates[i].Email > candidates[j].Email
	})
	return candidates[0], true
}

func TestAcquireForImageWaitsForOccupiedAccount(t *testing.T) {
	store := NewStore([]Account{{Email: "one@example.com", AccessToken: "one", CreatedAt: 1}}, "")
	first, err := store.AcquireForImage(context.Background(), nil, nil)
	if err != nil || first.AccessToken != "one" {
		t.Fatalf("first=%#v err=%v", first, err)
	}

	waiting := make(chan struct{}, 1)
	type result struct {
		account Account
		err     error
	}
	done := make(chan result, 1)
	go func() {
		account, err := store.AcquireForImage(context.Background(), nil, func() { waiting <- struct{}{} })
		done <- result{account: account, err: err}
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("second task did not enter the account queue")
	}
	select {
	case result := <-done:
		t.Fatalf("occupied account was reused: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}

	store.ReleaseImage(first.AccessToken)
	select {
	case result := <-done:
		if result.err != nil || result.account.AccessToken != "one" {
			t.Fatalf("result=%#v", result)
		}
		store.ReleaseImage(result.account.AccessToken)
	case <-time.After(time.Second):
		t.Fatal("queued task did not acquire after release")
	}
}

func TestAcquireForImageWaitersAreFIFO(t *testing.T) {
	store := NewStore([]Account{{Email: "one@example.com", AccessToken: "one", CreatedAt: 1}}, "")
	first, err := store.AcquireForImage(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		account Account
		err     error
	}
	firstWaiting := make(chan struct{}, 1)
	secondWaiting := make(chan struct{}, 1)
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		account, err := store.AcquireForImage(context.Background(), nil, func() { firstWaiting <- struct{}{} })
		firstDone <- result{account: account, err: err}
	}()
	select {
	case <-firstWaiting:
	case <-time.After(time.Second):
		t.Fatal("first waiter did not queue")
	}
	go func() {
		account, err := store.AcquireForImage(context.Background(), nil, func() { secondWaiting <- struct{}{} })
		secondDone <- result{account: account, err: err}
	}()
	select {
	case <-secondWaiting:
	case <-time.After(time.Second):
		t.Fatal("second waiter did not queue")
	}

	store.ReleaseImage(first.AccessToken)
	var acquired Account
	select {
	case got := <-firstDone:
		if got.err != nil || got.account.AccessToken != "one" {
			t.Fatalf("first result=%#v", got)
		}
		acquired = got.account
	case got := <-secondDone:
		t.Fatalf("later waiter overtook the queue: %#v", got)
	case <-time.After(time.Second):
		t.Fatal("first waiter did not acquire")
	}
	select {
	case got := <-secondDone:
		t.Fatalf("second waiter acquired before the first released: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}

	store.ReleaseImage(acquired.AccessToken)
	select {
	case got := <-secondDone:
		if got.err != nil || got.account.AccessToken != "one" {
			t.Fatalf("second result=%#v", got)
		}
		store.ReleaseImage(got.account.AccessToken)
	case <-time.After(time.Second):
		t.Fatal("second waiter did not acquire after first release")
	}
}

func TestAcquireForImageCanceledWaiterIsRemovedFromFIFO(t *testing.T) {
	store := NewStore([]Account{{Email: "one@example.com", AccessToken: "one", CreatedAt: 1}}, "")
	first, err := store.AcquireForImage(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		account Account
		err     error
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstWaiting := make(chan struct{}, 1)
	secondWaiting := make(chan struct{}, 1)
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		account, err := store.AcquireForImage(firstCtx, nil, func() { firstWaiting <- struct{}{} })
		firstDone <- result{account: account, err: err}
	}()
	select {
	case <-firstWaiting:
	case <-time.After(time.Second):
		t.Fatal("first waiter did not queue")
	}
	go func() {
		account, err := store.AcquireForImage(context.Background(), nil, func() { secondWaiting <- struct{}{} })
		secondDone <- result{account: account, err: err}
	}()
	select {
	case <-secondWaiting:
	case <-time.After(time.Second):
		t.Fatal("second waiter did not queue")
	}

	cancelFirst()
	select {
	case got := <-firstDone:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("first result=%#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not exit")
	}
	store.mu.RLock()
	queued := len(store.imageWaiters)
	store.mu.RUnlock()
	if queued != 1 {
		t.Fatalf("queued=%d, want only the surviving waiter", queued)
	}

	store.ReleaseImage(first.AccessToken)
	select {
	case got := <-secondDone:
		if got.err != nil || got.account.AccessToken != "one" {
			t.Fatalf("second result=%#v", got)
		}
		store.ReleaseImage(got.account.AccessToken)
	case <-time.After(time.Second):
		t.Fatal("surviving waiter did not acquire")
	}
}

func TestAcquireForImageFIFOWaitsForHeadCooldown(t *testing.T) {
	until := time.Now().Add(100 * time.Millisecond)
	store := NewStore([]Account{{AccessToken: "one", Extra: map[string]any{imageCooldownUntilKey: until.UTC().Format(time.RFC3339Nano)}}}, "")
	store.now = time.Now
	type result struct {
		account Account
		err     error
	}
	firstWaiting := make(chan struct{}, 1)
	secondWaiting := make(chan struct{}, 1)
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		account, err := store.AcquireForImage(context.Background(), nil, func() { firstWaiting <- struct{}{} })
		firstDone <- result{account: account, err: err}
	}()
	select {
	case <-firstWaiting:
	case <-time.After(time.Second):
		t.Fatal("first waiter did not enter cooldown wait")
	}
	go func() {
		account, err := store.AcquireForImage(context.Background(), nil, func() { secondWaiting <- struct{}{} })
		secondDone <- result{account: account, err: err}
	}()
	select {
	case <-secondWaiting:
	case <-time.After(time.Second):
		t.Fatal("second waiter did not queue")
	}

	select {
	case got := <-firstDone:
		if got.err != nil || got.account.AccessToken != "one" {
			t.Fatalf("first result=%#v", got)
		}
		store.ReleaseImage(got.account.AccessToken)
	case got := <-secondDone:
		t.Fatalf("later waiter overtook cooldown head: %#v", got)
	case <-time.After(time.Second):
		t.Fatal("first waiter did not acquire after cooldown")
	}
	select {
	case got := <-secondDone:
		if got.err != nil || got.account.AccessToken != "one" {
			t.Fatalf("second result=%#v", got)
		}
		store.ReleaseImage(got.account.AccessToken)
	case <-time.After(time.Second):
		t.Fatal("second waiter did not acquire after first release")
	}
}

func TestAcquireForImageWaitsForCooldownExpiry(t *testing.T) {
	until := time.Now().Add(60 * time.Millisecond)
	store := NewStore([]Account{{
		Email:       "one@example.com",
		AccessToken: "one",
		CreatedAt:   1,
		Extra:       map[string]any{imageCooldownUntilKey: until.UTC().Format(time.RFC3339Nano)},
	}}, "")
	store.now = time.Now
	waiting := make(chan struct{}, 1)
	started := time.Now()
	account, err := store.AcquireForImage(context.Background(), nil, func() { waiting <- struct{}{} })
	if err != nil || account.AccessToken != "one" {
		t.Fatalf("account=%#v err=%v", account, err)
	}
	if elapsed := time.Since(started); elapsed < 35*time.Millisecond {
		t.Fatalf("acquired before cooldown expired: %v", elapsed)
	}
	select {
	case <-waiting:
	default:
		t.Fatal("task did not enter cooldown wait")
	}
	store.ReleaseImage(account.AccessToken)
}

func TestAcquireForImageCooldownWaitRespectsContext(t *testing.T) {
	store := NewStore([]Account{{
		AccessToken: "one",
		Extra:       map[string]any{imageCooldownUntilKey: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)},
	}}, "")
	store.now = time.Now
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := store.AcquireForImage(ctx, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestAcquireForImageLeaseReleaseBeatsCooldownTimer(t *testing.T) {
	store := NewStore([]Account{
		{AccessToken: "leased", CreatedAt: 2, Extra: map[string]any{}},
		{AccessToken: "cooling", CreatedAt: 1, Extra: map[string]any{imageCooldownUntilKey: time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)}},
	}, "")
	store.now = time.Now
	leased, err := store.AcquireForImage(context.Background(), nil, nil)
	if err != nil || leased.AccessToken != "leased" {
		t.Fatalf("leased=%#v err=%v", leased, err)
	}

	waiting := make(chan struct{}, 1)
	type result struct {
		account Account
		err     error
	}
	done := make(chan result, 1)
	go func() {
		account, err := store.AcquireForImage(context.Background(), nil, func() { waiting <- struct{}{} })
		done <- result{account: account, err: err}
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("task did not wait for availability")
	}

	store.ReleaseImage(leased.AccessToken)
	select {
	case got := <-done:
		if got.err != nil || got.account.AccessToken != "leased" {
			t.Fatalf("result=%#v", got)
		}
		store.ReleaseImage(got.account.AccessToken)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lease release did not wake cooldown waiter")
	}
}

func TestAcquireAccountForImageWaitsForOccupiedAccount(t *testing.T) {
	store := NewStore([]Account{{Email: "one@example.com", AccessToken: "one", CreatedAt: 1}}, "")
	first, err := store.AcquireAccountForImage(context.Background(), "one", nil)
	if err != nil || first.AccessToken != "one" {
		t.Fatalf("first=%#v err=%v", first, err)
	}

	waiting := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := store.AcquireAccountForImage(context.Background(), "one", func() { waiting <- struct{}{} })
		done <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("specific account task did not enter the queue")
	}

	store.ReleaseImage(first.AccessToken)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("specific queued task did not acquire after release")
	}
}

func TestRemoveInvalidTokenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	store := NewStore([]Account{{Email: "a", AccessToken: "tok", CreatedAt: 1}}, path)
	defer store.Close()
	removed, err := store.RemoveInvalidToken("tok", "token_revoked")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if _, err := store.SelectForImage(nil); !errors.Is(err, ErrNoAvailableAccount) {
		t.Fatalf("want no account got %v", err)
	}
	logs := store.CredentialRecoveryLogs("", 10)
	if len(logs) != 1 || logs[0].Event != "account_deleted" || logs[0].Error != "token_revoked" {
		t.Fatalf("logs=%#v", logs)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"accounts"`) {
		t.Fatalf("bad saved data: %s", data)
	}
}

func TestPublicAccountIncludesDerivedHealth(t *testing.T) {
	account := Account{AccessToken: "token", Status: "正常", Quota: 25, Extra: map[string]any{}}
	public := account.Public()
	if public["health_score"] != float64(100) || public["health_label"] != "优秀" {
		t.Fatalf("health=%#v", public)
	}
}

func TestMarkImageSuccessUpdatesKnownQuotaEstimate(t *testing.T) {
	store := NewStore([]Account{{
		AccessToken: "token",
		Quota:       25,
		Extra: map[string]any{
			"limits_progress": []map[string]any{{"feature_name": "image_gen", "remaining": 25}},
		},
	}}, "")
	if err := store.MarkImageSuccess("token"); err != nil {
		t.Fatal(err)
	}
	account, found := store.Get("token")
	if !found || account.Quota != 24 || account.ImageOK != 1 || asInt(account.Extra["image_quota_total"]) != 25 {
		t.Fatalf("account=%#v found=%v", account, found)
	}
	progress := account.Extra["limits_progress"].([]map[string]any)
	if asInt(progress[0]["remaining"]) != 24 {
		t.Fatalf("limits_progress=%#v", progress)
	}
}

func TestMarkImageQuotaExhaustedRetainsAccount(t *testing.T) {
	store := NewStore([]Account{{AccessToken: "token", Quota: 1, Status: "正常", Extra: map[string]any{}}}, "")
	if err := store.MarkImageQuotaExhausted("token", errors.New("no available free image quota")); err != nil {
		t.Fatal(err)
	}
	account, found := store.Get("token")
	if !found || account.Status != "限流" || account.Quota != 0 {
		t.Fatalf("account=%#v found=%v", account, found)
	}
}

func TestMarkImageSuccessDoesNotBlockOnPersistence(t *testing.T) {
	state := &blockingAccountStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	store := NewStoreWithPersistence([]Account{{AccessToken: "token", ImageQuotaUnknown: true}}, state)
	released := false
	defer func() {
		if !released {
			close(state.release)
		}
		store.Close()
	}()

	state.mu.Lock()
	state.block = true
	state.mu.Unlock()
	if err := store.MarkImageSuccess("token"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-state.started:
	case <-time.After(time.Second):
		t.Fatal("account persistence worker did not start")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 50)
	started := time.Now()
	for index := 0; index < 50; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.MarkImageSuccess("token")
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
		t.Fatal("50 MarkImageSuccess calls blocked on persistence")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("50 MarkImageSuccess calls blocked on persistence for %s", elapsed)
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if account, found := store.Get("token"); !found || account.ImageOK != 51 {
		t.Fatalf("in-memory account=%#v found=%v", account, found)
	}

	released = true
	close(state.release)
	store.Close()
	state.mu.Lock()
	persisted := state.saved
	state.mu.Unlock()
	if len(persisted.Accounts) != 1 || persisted.Accounts[0].ImageOK != 51 {
		t.Fatalf("persisted accounts=%#v", persisted.Accounts)
	}
}

func TestAccountSnapshotsDeepCopyNestedState(t *testing.T) {
	input := Account{
		AccessToken: "token",
		FP:          map[string]string{"user-agent": "original"},
		Extra: map[string]any{
			"nested":          map[string]any{"value": "original"},
			"limits_progress": []map[string]any{{"feature_name": "image_gen", "remaining": 5}},
			"items":           []any{map[string]any{"value": "original"}},
		},
	}
	store := NewStore([]Account{input}, "")

	input.FP["user-agent"] = "changed-input"
	input.Extra["nested"].(map[string]any)["value"] = "changed-input"
	input.Extra["limits_progress"].([]map[string]any)[0]["remaining"] = 0

	list := store.List()
	list[0].FP["user-agent"] = "changed-list"
	list[0].Extra["nested"].(map[string]any)["value"] = "changed-list"
	list[0].Extra["limits_progress"].([]map[string]any)[0]["remaining"] = 1
	list[0].Extra["items"].([]any)[0].(map[string]any)["value"] = "changed-list"

	selected, err := store.SelectForImage(nil)
	if err != nil {
		t.Fatal(err)
	}
	selected.Extra["nested"].(map[string]any)["value"] = "changed-selected"

	acquired, err := store.AcquireForImage(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	acquired.Extra["items"].([]any)[0].(map[string]any)["value"] = "changed-acquired"
	store.ReleaseImage(acquired.AccessToken)

	account, found := store.Get("token")
	if !found || account.FP["user-agent"] != "original" {
		t.Fatalf("account=%#v found=%v", account, found)
	}
	if value := account.Extra["nested"].(map[string]any)["value"]; value != "original" {
		t.Fatalf("nested value=%#v", value)
	}
	if value := account.Extra["limits_progress"].([]map[string]any)[0]["remaining"]; asInt(value) != 5 {
		t.Fatalf("limits_progress=%#v", account.Extra["limits_progress"])
	}
	if value := account.Extra["items"].([]any)[0].(map[string]any)["value"]; value != "original" {
		t.Fatalf("items value=%#v", value)
	}
}

func TestPublicListIsSafeDuringConcurrentImageUpdates(t *testing.T) {
	store := NewStore([]Account{{
		AccessToken: "token",
		Quota:       5000,
		Extra: map[string]any{
			"limits_progress": []map[string]any{{"feature_name": "image_gen", "remaining": 5000}},
		},
	}}, "")
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		for index := 0; index < 1000; index++ {
			_ = store.MarkImageSuccess("token")
		}
		close(done)
	}()
	<-started
	for index := 0; index < 1000; index++ {
		_ = store.PublicList()
		_ = store.List()
		_, _ = store.Get("token")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent image updates did not complete")
	}
}

func TestImageCooldownSkipsAndPersists(t *testing.T) {
	now := time.Date(2026, time.July, 14, 1, 2, 3, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "accounts.json")
	store := NewStore([]Account{
		{Email: "cool@example.com", AccessToken: "cool", CreatedAt: 2, Extra: map[string]any{}},
		{Email: "fallback@example.com", AccessToken: "fallback", CreatedAt: 1, Extra: map[string]any{}},
	}, path)
	defer store.Close()
	store.now = func() time.Time { return now }

	if err := store.MarkImageRateLimited("cool", 45*time.Second, errors.New("upstream 429")); err != nil {
		t.Fatal(err)
	}
	cooling, found := store.Get("cool")
	if !found || !isImageCooling(cooling, now) || imageCooldownUntil(cooling) != now.Add(45*time.Second) {
		t.Fatalf("cooling=%#v found=%v until=%v", cooling, found, imageCooldownUntil(cooling))
	}
	if got, err := store.SelectForImage(nil); err != nil || got.AccessToken != "fallback" {
		t.Fatalf("selected=%#v err=%v", got, err)
	}
	if _, err := store.AcquireAccountForImage(context.Background(), "cool", nil); !errors.Is(err, ErrNoAvailableAccount) {
		t.Fatalf("cooled specific account err=%v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	persisted, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer persisted.Close()
	persisted.now = func() time.Time { return now }
	if got, err := persisted.SelectForImage(nil); err != nil || got.AccessToken != "fallback" {
		t.Fatalf("persisted selected=%#v err=%v", got, err)
	}
	now = now.Add(46 * time.Second)
	if got, err := persisted.SelectForImage(nil); err != nil || got.AccessToken != "cool" {
		t.Fatalf("expired selected=%#v err=%v", got, err)
	}
}

func TestImageCooldownBackoffAndSuccessReset(t *testing.T) {
	now := time.Date(2026, time.July, 14, 2, 0, 0, 0, time.UTC)
	store := NewStore([]Account{{AccessToken: "token", Extra: map[string]any{}}}, "")
	store.now = func() time.Time { return now }

	if err := store.MarkImageHTTPFailure("token", 503, 0, errors.New("upstream 503")); err != nil {
		t.Fatal(err)
	}
	first, _ := store.Get("token")
	if got := imageCooldownUntil(first); !got.Equal(now.Add(15*time.Second)) || asInt(first.Extra[imageCooldownFailuresKey]) != 1 {
		t.Fatalf("first=%#v until=%v", first, got)
	}

	now = now.Add(time.Second)
	if err := store.MarkImageGenerationTerminated("token", errors.New("server_timeout")); err != nil {
		t.Fatal(err)
	}
	second, _ := store.Get("token")
	if got := imageCooldownUntil(second); !got.Equal(now.Add(40*time.Second)) || asInt(second.Extra[imageCooldownFailuresKey]) != 2 {
		t.Fatalf("second=%#v until=%v", second, got)
	}
	if second.Extra[imageCooldownReasonKey] != string(ImageCooldownGenerationTerminated) {
		t.Fatalf("reason=%#v", second.Extra[imageCooldownReasonKey])
	}

	if err := store.MarkImageSuccess("token"); err != nil {
		t.Fatal(err)
	}
	reset, _ := store.Get("token")
	for _, key := range []string{imageCooldownUntilKey, imageCooldownReasonKey, imageCooldownFailuresKey, imageCooldownLastErrorKey, imageCooldownLastAtKey} {
		if _, exists := reset.Extra[key]; exists {
			t.Fatalf("cooldown key %q remained after success: %#v", key, reset.Extra)
		}
	}
}

func TestMarkImageHTTPFailureIgnoresNonTransientStatus(t *testing.T) {
	store := NewStore([]Account{{AccessToken: "token", Extra: map[string]any{}}}, "")
	if err := store.MarkImageHTTPFailure("token", 400, 0, errors.New("bad request")); err != nil {
		t.Fatal(err)
	}
	account, found := store.Get("token")
	if !found || account.ImageFailures != 0 || len(account.Extra) != 0 {
		t.Fatalf("account=%#v found=%v", account, found)
	}
}

func TestEnsureBrowserIdentityPersistsStableValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	store := NewStore([]Account{{AccessToken: "token"}}, path)
	defer store.Close()
	first, found, err := store.EnsureBrowserIdentity("token")
	if err != nil || !found || first.DeviceID == "" || first.SessionID == "" || first.UserAgent != DefaultBrowserUserAgent {
		t.Fatalf("account=%#v found=%v err=%v", first, found, err)
	}
	second, found, err := store.EnsureBrowserIdentity("token")
	if err != nil || !found || second.DeviceID != first.DeviceID || second.SessionID != first.SessionID || second.UserAgent != first.UserAgent {
		t.Fatalf("account=%#v found=%v err=%v", second, found, err)
	}
	persisted, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer persisted.Close()
	loaded, found := persisted.Get("token")
	if !found || loaded.DeviceID != first.DeviceID || loaded.SessionID != first.SessionID || loaded.UserAgent != first.UserAgent {
		t.Fatalf("loaded=%#v found=%v", loaded, found)
	}
}

func TestImageDispatchStatsCountsLeasesCoolingAndDeadAccounts(t *testing.T) {
	now := time.Date(2026, time.July, 19, 2, 0, 0, 0, time.UTC)
	store := NewStore([]Account{
		{Email: "idle@example.com", AccessToken: "idle", Status: "正常", Quota: 3, ImageOK: 3, ImageFailures: 1},
		{Email: "leased@example.com", AccessToken: "leased", Status: "正常", Quota: 2},
		{Email: "cool@example.com", AccessToken: "cool", Status: "正常", Quota: 4, Extra: map[string]any{}},
		{Email: "invalid@example.com", AccessToken: "invalid", Status: StatusCredentialInvalid},
		{Email: "abnormal@example.com", AccessToken: "abnormal", Status: "异常"},
		{Email: "limited@example.com", AccessToken: "limited", Status: "限流"},
		{Email: "disabled@example.com", AccessToken: "disabled", Status: "正常", Disabled: true},
	}, "")
	store.now = func() time.Time { return now }

	leased, err := store.AcquireAccountForImage(context.Background(), "leased", nil)
	if err != nil || leased.AccessToken != "leased" {
		t.Fatalf("leased=%#v err=%v", leased, err)
	}
	if err := store.MarkImageUpstreamFailure("cool", errors.New("upstream 502")); err != nil {
		t.Fatal(err)
	}

	stats := store.ImageDispatchStats()
	if stats.Total != 7 || stats.Usable != 3 || stats.Dispatchable != 2 || stats.Idle != 1 || stats.Leased != 1 || stats.Cooling != 1 {
		t.Fatalf("dispatch stats=%#v", stats)
	}
	if stats.Dead != 2 || stats.Invalid != 1 || stats.Abnormal != 1 || stats.Limited != 1 || stats.Disabled != 1 {
		t.Fatalf("status stats=%#v", stats)
	}
	if stats.KnownRemainingQuota != 9 || stats.KnownQuotaAccounts != 3 {
		t.Fatalf("quota stats=%#v", stats)
	}
	if stats.DeadRate != 28.57 || stats.CoolingRate != 33.33 || stats.HistoricalFailureRate == 0 {
		t.Fatalf("rates=%#v", stats)
	}
	if stats.NextCooldownEndsAt == nil || !stats.NextCooldownEndsAt.After(now) {
		t.Fatalf("next cooldown=%v", stats.NextCooldownEndsAt)
	}
}
