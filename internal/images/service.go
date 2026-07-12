package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/openaiweb"
	"imagepool/internal/storage"
)

type Service struct {
	cfgMu        sync.RWMutex
	cfg          config.Config
	store        *accounts.Store
	backend      openaiweb.Backend
	storage      *storage.Service
	precheckMu   sync.Mutex
	prechecks    map[string]*accountPrecheckFlight
	precheckGate *accountPrecheckGate
}

type accountPrecheckFlight struct {
	done    chan struct{}
	account accounts.Account
	err     error
}

// accountPrecheckGate bounds expensive Web/Sentinel verification work across
// all image tasks. It reads the current configured limit on each acquisition,
// so settings changes apply without restarting the service.
type accountPrecheckGate struct {
	mu     sync.Mutex
	active int
	wake   chan struct{}
}

func newAccountPrecheckGate() *accountPrecheckGate {
	return &accountPrecheckGate{wake: make(chan struct{})}
}

func (g *accountPrecheckGate) acquire(ctx context.Context, limit int, onWait func()) (func(), error) {
	if limit <= 0 {
		limit = 1
	}
	waited := false
	for {
		g.mu.Lock()
		if g.active < limit {
			g.active++
			g.mu.Unlock()
			return g.release, nil
		}
		wake := g.wake
		g.mu.Unlock()
		if !waited && onWait != nil {
			onWait()
			waited = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (g *accountPrecheckGate) release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	close(g.wake)
	g.wake = make(chan struct{})
	g.mu.Unlock()
}

func (g *accountPrecheckGate) notify() {
	if g == nil {
		return
	}
	g.mu.Lock()
	close(g.wake)
	g.wake = make(chan struct{})
	g.mu.Unlock()
}

type accountInfoBackend interface {
	GetAccountInfo(context.Context, string) (openaiweb.AccountInfo, error)
}

type accountInfoForBackend interface {
	GetAccountInfoFor(context.Context, accounts.Account) (openaiweb.AccountInfo, error)
}

type imageReadinessBackend interface {
	CheckImageReady(context.Context, string) error
}

type imageReadinessForBackend interface {
	CheckImageReadyFor(context.Context, accounts.Account) error
}

type accountModelsForBackend interface {
	ListModelsFor(context.Context, accounts.Account) ([]string, error)
}

type Request = openaiweb.ImageRequest

type Data struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type Response struct {
	Created        int64                  `json:"created"`
	Data           []Data                 `json:"data"`
	AccountEmail   string                 `json:"account_email,omitempty"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	BackendModel   string                 `json:"backend_model,omitempty"`
	Attempts       []openaiweb.AttemptLog `json:"attempts,omitempty"`
	ImageRoute     map[string]any         `json:"image_route,omitempty"`
}

func NewService(cfg config.Config, store *accounts.Store, backend openaiweb.Backend, imageStorage ...*storage.Service) *Service {
	cfg = cfg.Normalize()
	service := &Service{cfg: cfg, store: store, backend: backend, prechecks: map[string]*accountPrecheckFlight{}, precheckGate: newAccountPrecheckGate()}
	if len(imageStorage) > 0 {
		service.storage = imageStorage[0]
	}
	return service
}

func (s *Service) UpdateConfig(cfg config.Config) {
	if s == nil {
		return
	}
	s.cfgMu.Lock()
	s.cfg = cfg.Normalize()
	s.cfgMu.Unlock()
	s.precheckGate.notify()
}

func (s *Service) currentConfig() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Service) Generate(ctx context.Context, req Request) (Response, error) {
	if req.N <= 0 {
		req.N = 1
	}
	if req.N > 4 {
		req.N = 4
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.Quality == "" {
		req.Quality = "auto"
	}
	if req.ResponseFormat == "" {
		req.ResponseFormat = "url"
	}
	if req.N == 1 {
		result, err := s.generateOne(ctx, req)
		if err != nil {
			return Response{}, err
		}
		return responseFromResult(result), nil
	}
	var wg sync.WaitGroup
	results := make([]openaiweb.ImageResult, req.N)
	errs := make([]error, req.N)
	for i := 0; i < req.N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			single := req
			single.N = 1
			results[i], errs[i] = s.generateOne(ctx, single)
		}(i)
	}
	wg.Wait()
	combined := Response{Created: time.Now().Unix()}
	for i, err := range errs {
		if err != nil {
			return Response{}, err
		}
		part := responseFromResult(results[i])
		combined.Data = append(combined.Data, part.Data...)
		combined.Attempts = append(combined.Attempts, part.Attempts...)
		if combined.AccountEmail == "" {
			combined.AccountEmail = part.AccountEmail
		}
		if combined.ConversationID == "" {
			combined.ConversationID = part.ConversationID
		}
		if combined.BackendModel == "" {
			combined.BackendModel = part.BackendModel
		}
	}
	return combined, nil
}

func (s *Service) CheckAccount(ctx context.Context, token string) (accounts.AccountCheckResult, error) {
	result := accounts.AccountCheckResult{ImageQuotaUnknown: true}
	account, found := s.store.Get(token)
	if !found {
		return result, fmt.Errorf("account not found")
	}
	var err error
	account, err = s.ensureBrowserIdentity(account)
	if err != nil {
		return result, err
	}
	if err := s.checkImageReadiness(ctx, account); err != nil {
		return result, err
	}
	return s.checkAccountDetails(ctx, account, result, true)
}

func (s *Service) ensureBrowserIdentity(account accounts.Account) (accounts.Account, error) {
	updated, found, err := s.store.EnsureBrowserIdentity(account.AccessToken)
	if err != nil {
		return account, err
	}
	if !found {
		return account, fmt.Errorf("account not found")
	}
	return updated, nil
}

func (s *Service) checkImageReadiness(ctx context.Context, account accounts.Account) error {
	if backend, ok := s.backend.(imageReadinessForBackend); ok {
		return backend.CheckImageReadyFor(ctx, account)
	}
	if backend, ok := s.backend.(imageReadinessBackend); ok {
		return backend.CheckImageReady(ctx, account.AccessToken)
	}
	return nil
}

func (s *Service) checkAccountDetails(ctx context.Context, account accounts.Account, result accounts.AccountCheckResult, includeModels bool) (accounts.AccountCheckResult, error) {
	if backend, ok := s.backend.(accountInfoForBackend); ok {
		info, err := backend.GetAccountInfoFor(ctx, account)
		if err != nil {
			return result, err
		}
		result.Email = info.Email
		result.Type = info.Type
		result.Quota = info.Quota
		result.ImageQuotaUnknown = info.ImageQuotaUnknown
		result.LimitsProgress = info.LimitsProgress
		result.RestoreAt = info.RestoreAt
		result.DefaultModelSlug = info.DefaultModelSlug
	} else if backend, ok := s.backend.(accountInfoBackend); ok {
		info, err := backend.GetAccountInfo(ctx, account.AccessToken)
		if err != nil {
			return result, err
		}
		result.Email = info.Email
		result.Type = info.Type
		result.Quota = info.Quota
		result.ImageQuotaUnknown = info.ImageQuotaUnknown
		result.LimitsProgress = info.LimitsProgress
		result.RestoreAt = info.RestoreAt
		result.DefaultModelSlug = info.DefaultModelSlug
	}
	if includeModels {
		var models []string
		var err error
		if backend, ok := s.backend.(accountModelsForBackend); ok {
			models, err = backend.ListModelsFor(ctx, account)
		} else {
			models, err = s.backend.ListModels(ctx, account.AccessToken)
		}
		if err != nil {
			return result, err
		}
		result.Models = models
	}
	return result, nil
}

func (s *Service) GenerateWithAccount(ctx context.Context, token string, req Request) (Response, error) {
	account, ok := s.store.Get(token)
	if !ok {
		return Response{}, fmt.Errorf("account not found")
	}
	account, err := s.store.AcquireAccountForImage(ctx, token, func() {
		reportAccountWait(req, account)
	})
	if err != nil {
		return Response{}, err
	}
	defer s.store.ReleaseImage(account.AccessToken)
	if req.N <= 0 {
		req.N = 1
	}
	account, err = s.precheckAccount(ctx, account, req)
	if err != nil {
		reportAccountPrecheckFailure(req, account, err)
		return Response{}, err
	}
	reportAccountProgress(req, "account_validated", "账号 Token 验证通过", account)
	result, err := s.backend.GenerateImage(ctx, account, req)
	if err != nil {
		if !openaiweb.IsInteractiveChallengeError(err) {
			_ = s.store.MarkFailure(account.AccessToken, err)
		}
		if openaiweb.IsAuthenticationError(err) {
			_, _ = s.store.RemoveInvalidToken(account.AccessToken, err.Error())
		} else if openaiweb.IsNoFreeImageQuotaError(err) {
			_ = s.store.MarkImageQuotaExhausted(account.AccessToken, err)
		}
		return Response{}, err
	}
	result = s.cacheResult(ctx, account, result, req.OutputBaseURL)
	_ = s.store.MarkImageSuccess(account.AccessToken)
	return responseFromResult(result), nil
}

func (s *Service) generateOne(ctx context.Context, req Request) (openaiweb.ImageResult, error) {
	exclude := map[string]bool{}
	attempts := []openaiweb.AttemptLog{}
	maxAttempts := s.currentConfig().MaxImageAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	var lastErr error
	imageAttempts := 0
	for imageAttempts < maxAttempts {
		account, err := s.store.AcquireForImage(ctx, exclude, func() {
			reportAccountWait(req, accounts.Account{})
		})
		if err != nil {
			if lastErr != nil {
				return openaiweb.ImageResult{Attempts: attempts}, fmt.Errorf("%w; attempts=%v", lastErr, attempts)
			}
			return openaiweb.ImageResult{Attempts: attempts}, err
		}
		exclude[account.AccessToken] = true
		log := openaiweb.AttemptLog{Attempt: len(attempts) + 1, AccountEmail: account.Email, Status: "running"}
		account, err = s.precheckAccount(ctx, account, req)
		if err != nil {
			s.store.ReleaseImage(account.AccessToken)
			lastErr = err
			log.Status = "failed"
			log.Error = err.Error()
			if openaiweb.IsAuthenticationError(err) {
				removed, _ := s.store.RemoveInvalidToken(account.AccessToken, err.Error())
				log.RemovedAccount = removed
			}
			if openaiweb.IsNoFreeImageQuotaError(err) {
				_ = s.store.MarkImageQuotaExhausted(account.AccessToken, err)
			}
			attempts = append(attempts, log)
			reportAccountPrecheckFailure(req, account, err)
			if isTransientPrecheckError(err) {
				return openaiweb.ImageResult{Attempts: attempts}, fmt.Errorf("账号预检暂时不可用（代理或上游超时）: %w", err)
			}
			if openaiweb.IsInteractiveChallengeError(err) {
				return openaiweb.ImageResult{Attempts: attempts}, err
			}
			continue
		}
		log.AccountEmail = account.Email
		reportAccountProgress(req, "account_validated", "账号 Token 验证通过", account)
		imageAttempts++
		result, err := s.backend.GenerateImage(ctx, account, req)
		if err == nil {
			result = s.cacheResult(ctx, account, result, req.OutputBaseURL)
			_ = s.store.MarkImageSuccess(account.AccessToken)
			s.store.ReleaseImage(account.AccessToken)
			log.Status = "success"
			log.BackendModel = result.BackendModel
			log.ConversationID = result.ConversationID
			attempts = append(attempts, log)
			result.Attempts = append(result.Attempts, attempts...)
			return result, nil
		}
		lastErr = err
		log.Status = "failed"
		log.Error = err.Error()
		if !openaiweb.IsInteractiveChallengeError(err) {
			_ = s.store.MarkFailure(account.AccessToken, err)
		}
		if openaiweb.IsAuthenticationError(err) {
			removed, _ := s.store.RemoveInvalidToken(account.AccessToken, err.Error())
			log.RemovedAccount = removed
		} else if openaiweb.IsNoFreeImageQuotaError(err) {
			_ = s.store.MarkImageQuotaExhausted(account.AccessToken, err)
		}
		s.store.ReleaseImage(account.AccessToken)
		attempts = append(attempts, log)
		if openaiweb.IsInteractiveChallengeError(err) {
			return openaiweb.ImageResult{Attempts: attempts}, err
		}
		if !openaiweb.IsRetryableImageError(err) {
			return openaiweb.ImageResult{Attempts: attempts}, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("image generation failed")
	}
	return openaiweb.ImageResult{Attempts: attempts}, lastErr
}

func (s *Service) precheckAccount(ctx context.Context, account accounts.Account, req Request) (accounts.Account, error) {
	cfg := s.currentConfig()
	timeout := time.Duration(cfg.ImageAccountPrecheckTimeoutSecs * float64(time.Second))
	if timeout <= 0 {
		timeout = 75 * time.Second
	}
	precheckCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// The deadline covers both the global verification queue and the upstream
	// checks. Otherwise a burst can wait several full verification windows.
	release, err := s.precheckGate.acquire(precheckCtx, cfg.ImageAccountPrecheckConcurrency, func() {
		reportAccountPrecheckWait(req, account, cfg.ImageAccountPrecheckConcurrency)
	})
	if err != nil {
		return account, err
	}
	defer release()
	reportAccountProgress(req, "checking_account", "验证账号 Token", account)

	account, err = s.ensureBrowserIdentity(account)
	if err != nil {
		return account, err
	}
	token := strings.TrimSpace(account.AccessToken)
	if token == "" {
		return account, fmt.Errorf("access token is required")
	}
	if err := s.checkImageReadiness(precheckCtx, account); err != nil {
		return s.handleAccountPrecheckError(account, err)
	}
	if !s.accountNeedsPrecheck(account) {
		return account, nil
	}

	s.precheckMu.Lock()
	if pending := s.prechecks[token]; pending != nil {
		s.precheckMu.Unlock()
		select {
		case <-pending.done:
			return pending.account, pending.err
		case <-precheckCtx.Done():
			return account, precheckCtx.Err()
		}
	}
	pending := &accountPrecheckFlight{done: make(chan struct{})}
	s.prechecks[token] = pending
	s.precheckMu.Unlock()

	updated, err := s.refreshAccountForImage(precheckCtx, account)
	s.precheckMu.Lock()
	pending.account = updated
	pending.err = err
	delete(s.prechecks, token)
	close(pending.done)
	s.precheckMu.Unlock()
	return updated, err
}

func (s *Service) accountNeedsPrecheck(account accounts.Account) bool {
	interval := s.currentConfig().ImageAccountPrecheckIntervalMinutes
	if interval <= 0 || strings.TrimSpace(account.Status) == "" || account.Extra == nil {
		return true
	}
	if refreshRequired, _ := account.Extra["image_quota_refresh_required"].(bool); refreshRequired {
		return true
	}
	raw := strings.TrimSpace(fmt.Sprint(account.Extra["last_account_refresh_at"]))
	last, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return true
	}
	return time.Since(last) >= time.Duration(interval)*time.Minute
}

func (s *Service) refreshAccountForImage(ctx context.Context, account accounts.Account) (accounts.Account, error) {
	check, err := s.checkAccountDetails(ctx, account, accounts.AccountCheckResult{ImageQuotaUnknown: true}, false)
	if err != nil {
		return s.handleAccountPrecheckError(account, err)
	}
	updated, found, recordErr := s.store.RecordRefresh(account.AccessToken, check, nil)
	if recordErr != nil {
		return account, recordErr
	}
	if !found {
		return account, fmt.Errorf("account no longer available")
	}
	if !check.ImageQuotaUnknown && check.Quota <= 0 {
		err := fmt.Errorf("no available free image quota")
		_ = s.store.MarkImageQuotaExhausted(updated.AccessToken, err)
		return updated, err
	}
	return updated, nil
}

func (s *Service) handleAccountPrecheckError(account accounts.Account, err error) (accounts.Account, error) {
	if isTransientPrecheckError(err) {
		return account, err
	}
	if !openaiweb.IsInteractiveChallengeError(err) {
		if openaiweb.IsAuthenticationError(err) {
			_, _ = s.store.RemoveInvalidToken(account.AccessToken, err.Error())
		} else {
			_, _, _ = s.store.RecordRefresh(account.AccessToken, accounts.AccountCheckResult{}, err)
		}
	}
	return account, err
}

func isTransientPrecheckError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary()) {
		return true
	}
	var upstream *openaiweb.UpstreamError
	if errors.As(err, &upstream) && (upstream.StatusCode == 408 || upstream.StatusCode == 429 || upstream.StatusCode >= 500) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "timeout") ||
		strings.Contains(message, "deadline exceeded") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "proxyconnect") ||
		strings.Contains(message, "temporarily unavailable")
}

func reportAccountProgress(req Request, progress, message string, account accounts.Account) {
	if req.Progress == nil {
		return
	}
	details := map[string]any{}
	if account.Email != "" {
		details["account_email"] = account.Email
	}
	req.Progress(openaiweb.ProgressEvent{Progress: progress, Message: message, Details: details})
}

func reportAccountWait(req Request, account accounts.Account) {
	reportAccountProgress(req, "waiting_account", "暂无空闲账号，任务排队等待", account)
}

func reportAccountPrecheckWait(req Request, account accounts.Account, limit int) {
	message := "等待账号验证队列"
	if limit > 0 {
		message = fmt.Sprintf("等待账号验证队列（并发上限 %d）", limit)
	}
	reportAccountProgress(req, "waiting_account_precheck", message, account)
}

func reportAccountPrecheckFailure(req Request, account accounts.Account, err error) {
	if req.Progress == nil {
		return
	}
	details := map[string]any{"error": err.Error()}
	if account.Email != "" {
		details["account_email"] = account.Email
	}
	message := "账号 Token 验证失败，切换账号"
	if openaiweb.IsAuthenticationError(err) {
		message = "账号凭证失效，已标记失效并交由后台恢复，切换账号"
	} else if isTransientPrecheckError(err) {
		message = "账号预检超时或代理不可用，任务未切换账号"
	}
	req.Progress(openaiweb.ProgressEvent{Progress: "account_precheck_failed", Message: message, Details: details})
}

type imageDownloader interface {
	DownloadImageFor(ctx context.Context, account accounts.Account, imageURL string) ([]byte, error)
}

func (s *Service) cacheResult(ctx context.Context, account accounts.Account, result openaiweb.ImageResult, baseURL string) openaiweb.ImageResult {
	if s.storage == nil || len(result.URLs) == 0 {
		return result
	}
	downloader, ok := s.backend.(imageDownloader)
	if !ok {
		return result
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	urls := make([]string, 0, len(result.URLs))
	for _, remoteURL := range result.URLs {
		data, err := downloader.DownloadImageFor(ctx, account, remoteURL)
		if err != nil {
			log.Printf("image cache download failed: %v", err)
			urls = append(urls, remoteURL)
			continue
		}
		item, err := s.storage.Save(data)
		if err != nil {
			log.Printf("image cache save failed: %v", err)
			urls = append(urls, remoteURL)
			continue
		}
		encoded := url.PathEscape(item.Rel)
		if baseURL == "" {
			urls = append(urls, "/images/"+encoded)
		} else {
			urls = append(urls, baseURL+"/images/"+encoded)
		}
	}
	result.URLs = urls
	return result
}

func (s *Service) ListModels(ctx context.Context) ([]string, error) {
	base := append([]string(nil), s.currentConfig().Models...)
	account, err := s.store.SelectForImage(nil)
	if err != nil {
		return base, nil
	}
	account, err = s.ensureBrowserIdentity(account)
	if err != nil {
		return base, nil
	}
	var upstream []string
	if backend, ok := s.backend.(accountModelsForBackend); ok {
		upstream, err = backend.ListModelsFor(ctx, account)
	} else {
		upstream, err = s.backend.ListModels(ctx, account.AccessToken)
	}
	if err != nil {
		return base, nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, list := range [][]string{upstream, base} {
		for _, model := range list {
			if model != "" && !seen[model] {
				seen[model] = true
				out = append(out, model)
			}
		}
	}
	return out, nil
}

func responseFromResult(result openaiweb.ImageResult) Response {
	resp := Response{Created: time.Now().Unix(), AccountEmail: result.AccountEmail, ConversationID: result.ConversationID, BackendModel: result.BackendModel, Attempts: result.Attempts}
	for _, url := range result.URLs {
		resp.Data = append(resp.Data, Data{URL: url})
	}
	for _, b64 := range result.B64JSON {
		resp.Data = append(resp.Data, Data{B64JSON: b64})
	}
	resp.ImageRoute = map[string]any{"backend_model": result.BackendModel, "image_route": "free_image2_fallback"}
	return resp
}

func (r Response) MarshalForOpenAI() map[string]any {
	data, _ := json.Marshal(r)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
