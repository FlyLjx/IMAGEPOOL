package images

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deepteams/webp"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/limiters"
	"imagepool/internal/openaiweb"
	"imagepool/internal/storage"
)

const maxAuthenticationRetries = 10

// PublicImageModel is the stable model name exposed by IMAGE POOL. Upstream
// Web slugs remain internal routing details and can vary by account type.
const PublicImageModel = "gpt-image-2"

type Service struct {
	cfgMu   sync.RWMutex
	cfg     config.Config
	store   *accounts.Store
	backend openaiweb.Backend
	storage *storage.Service
	global  *limiters.Gate
	stalled atomic.Uint64
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
	MimeType      string `json:"mime_type,omitempty"`
	Format        string `json:"format,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type Response struct {
	Created        int64                  `json:"created"`
	Data           []Data                 `json:"data"`
	Usage          *Usage                 `json:"usage,omitempty"`
	AccountEmail   string                 `json:"account_email,omitempty"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	BackendModel   string                 `json:"backend_model,omitempty"`
	Attempts       []openaiweb.AttemptLog `json:"attempts,omitempty"`
	ImageRoute     map[string]any         `json:"image_route,omitempty"`
}

type ConcurrencyStats struct {
	Global          limiters.Stats                  `json:"global"`
	Upstream        openaiweb.ImageConcurrencyStats `json:"upstream"`
	StalledAttempts uint64                          `json:"stalled_attempts"`
}

func NewService(cfg config.Config, store *accounts.Store, backend openaiweb.Backend, imageStorage ...*storage.Service) *Service {
	cfg = cfg.Normalize()
	if store != nil {
		store.SetImageMaxInflightPerAccount(cfg.ImageAccountMaxInflightPerAccount)
		store.SetImageDynamicSlots(cfg.ImageAccountDynamicSlots)
		store.ResetImageDynamicLimits()
	}
	service := &Service{cfg: cfg, store: store, backend: backend, global: limiters.New(cfg.ImageGlobalMaxInflight)}
	if len(imageStorage) > 0 {
		service.storage = imageStorage[0]
	}
	return service
}

func (s *Service) UpdateConfig(cfg config.Config) {
	if s == nil {
		return
	}
	next := cfg.Normalize()
	if s.store != nil {
		s.store.SetImageMaxInflightPerAccount(next.ImageAccountMaxInflightPerAccount)
		s.store.SetImageDynamicSlots(next.ImageAccountDynamicSlots)
		s.store.ResetImageDynamicLimits()
	}
	if s.global == nil {
		s.global = limiters.New(next.ImageGlobalMaxInflight)
	} else {
		s.global.SetLimit(next.ImageGlobalMaxInflight)
	}
	s.cfgMu.Lock()
	s.cfg = next
	s.cfgMu.Unlock()
}

func (s *Service) currentConfig() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Service) ConcurrencyStats() ConcurrencyStats {
	if s == nil {
		return ConcurrencyStats{}
	}
	stats := ConcurrencyStats{}
	if s.global != nil {
		stats.Global = s.global.Stats()
	}
	if backend, ok := s.backend.(interface {
		ImageConcurrencyStats() openaiweb.ImageConcurrencyStats
	}); ok {
		stats.Upstream = backend.ImageConcurrencyStats()
	}
	stats.StalledAttempts = s.stalled.Load()
	return stats
}

func (s *Service) Generate(ctx context.Context, req Request) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, publicModel := PrepareModelRequest(req)
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
	responseFormat, err := normalizeResponseFormat(req.ResponseFormat)
	if err != nil {
		return Response{}, err
	}
	if _, err := normalizeOutputFormat(req.OutputFormat); err != nil {
		return Response{}, err
	}
	req.ResponseFormat = responseFormat
	req.OutputFormat, _ = normalizeOutputFormat(req.OutputFormat)
	if req.N == 1 {
		result, err := s.generateOne(ctx, req)
		response := responseWithModel(responseFromResult(result), publicModel)
		if err != nil {
			return response, err
		}
		return withEstimatedUsage(response, req), nil
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
		part := responseFromResult(results[i])
		combined.Attempts = append(combined.Attempts, part.Attempts...)
		if err != nil {
			return combined, err
		}
		combined.Data = append(combined.Data, part.Data...)
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
	return withEstimatedUsage(responseWithModel(combined, publicModel), req), nil
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

// CheckAccountLight is used by scheduled refreshes. The actual image request
// remains authoritative for the image-specific Sentinel handshake.
func (s *Service) CheckAccountLight(ctx context.Context, token string) (accounts.AccountCheckResult, error) {
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
	// Scheduled refreshes only need to confirm the account and its quota.
	return s.checkAccountDetails(ctx, account, result, false)
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
	if ctx == nil {
		ctx = context.Background()
	}
	req, publicModel := PrepareModelRequest(req)
	account, ok := s.store.Get(token)
	if !ok {
		return Response{}, fmt.Errorf("account not found")
	}
	lease, err := s.store.AcquireAccountImageLease(ctx, token, imageDispatchRequirements(req), func() {
		reportAccountWait(req, account)
	})
	if err != nil {
		return Response{}, err
	}
	account = lease.Account
	releaseGlobal, err := s.acquireGlobal(ctx)
	if err != nil {
		s.store.ReleaseImageLease(lease.ID)
		return Response{}, err
	}
	defer releaseGlobal()
	taskCtx, cancel := s.taskContext(ctx, req)
	defer cancel()
	leaseCtx, cancelLeaseContext := bindImageLeaseContext(taskCtx, lease)
	defer cancelLeaseContext()
	released := false
	release := func() {
		if released {
			return
		}
		s.store.ReleaseImageLease(lease.ID)
		released = true
	}
	defer release()
	if req.N <= 0 {
		req.N = 1
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.Quality == "" {
		req.Quality = "auto"
	}
	responseFormat, err := normalizeResponseFormat(req.ResponseFormat)
	if err != nil {
		return Response{}, err
	}
	if _, err := normalizeOutputFormat(req.OutputFormat); err != nil {
		return Response{}, err
	}
	req.ResponseFormat = responseFormat
	req.OutputFormat, _ = normalizeOutputFormat(req.OutputFormat)
	account, err = s.prepareAccountForDispatch(account, req)
	if err != nil {
		return Response{}, err
	}
	result, err := s.backend.GenerateImage(leaseCtx, account, s.instrumentImageRequest(req, lease, account, nil))
	if err != nil {
		s.recordImageFailure(account.AccessToken, err)
		if openaiweb.IsAuthenticationError(err) {
			s.removeInvalidImageAccount(account.AccessToken, err)
		} else if openaiweb.IsNoFreeImageQuotaError(err) {
			_, _ = s.store.RemoveQuotaExhausted(account.AccessToken, err)
		}
		return Response{}, err
	}
	_ = s.store.MarkImageSuccess(account.AccessToken)
	s.reportAccountIdentity(req, account.AccessToken)
	// Downloads only need the immutable account identity; releasing the image
	// lease here lets the next queued generation start while the local cache is
	// populated.
	release()
	result, err = s.finalizeResult(taskCtx, account, result, req)
	response := responseWithModel(responseFromResult(result), publicModel)
	if err != nil {
		if openaiweb.IsAuthenticationError(err) {
			s.recordImageFailure(account.AccessToken, err)
			s.removeInvalidImageAccount(account.AccessToken, err)
		}
		return response, err
	}
	return withEstimatedUsage(response, req), nil
}

func (s *Service) taskContext(parent context.Context, req Request) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	cfg := s.currentConfig()
	timeout := time.Duration(cfg.ImageTaskTimeoutSecs * float64(time.Second))
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

func (s *Service) acquireGlobal(ctx context.Context) (func(), error) {
	if s == nil || s.global == nil {
		return func() {}, nil
	}
	return s.global.Acquire(ctx)
}

// bindImageLeaseContext starts the task budget after admission succeeds while
// still propagating an account eviction into the upstream request context.
// Waiting for a global/account slot is capacity backpressure, not generation
// time, so it must not consume the image task timeout.
func bindImageLeaseContext(taskCtx context.Context, lease accounts.ImageLease) (context.Context, context.CancelFunc) {
	if taskCtx == nil {
		taskCtx = context.Background()
	}
	boundCtx, cancelCause := context.WithCancelCause(taskCtx)
	go func() {
		select {
		case <-lease.Context.Done():
			cause := context.Cause(lease.Context)
			if cause == nil {
				cause = context.Canceled
			}
			cancelCause(cause)
		case <-boundCtx.Done():
		}
	}()
	return boundCtx, func() { cancelCause(nil) }
}

// acquireImageDispatch coordinates the global generation budget and account
// lease without holding either one while the other resource is unavailable.
func (s *Service) acquireImageDispatch(ctx context.Context, exclude map[string]bool, requirements accounts.ImageDispatchRequirements, onWait, onReady func()) (accounts.ImageLease, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if onReady != nil {
		onReady()
	}
	waitReported := false
	reportWait := func() {
		if waitReported || onWait == nil {
			return
		}
		waitReported = true
		onWait()
	}
	for {
		releaseGlobal, available := s.global.TryAcquire()
		if !available {
			reportWait()
			if err := s.global.Wait(ctx); err != nil {
				return accounts.ImageLease{}, nil, err
			}
			if onReady != nil {
				onReady()
			}
			waitReported = false
			continue
		}
		lease, acquired, err := s.store.TryAcquireImageLeaseWithRequirements(ctx, exclude, requirements)
		if err != nil {
			releaseGlobal()
			return accounts.ImageLease{}, nil, err
		}
		if acquired {
			return lease, releaseGlobal, nil
		}
		releaseGlobal()
		reportWait()
		if err := s.store.WaitForImageAvailabilityWithRequirements(ctx, exclude, requirements); err != nil {
			return accounts.ImageLease{}, nil, err
		}
		if onReady != nil {
			onReady()
		}
		waitReported = false
	}
}

func imageDispatchRequirements(req Request) accounts.ImageDispatchRequirements {
	return accounts.ImageDispatchRequirements{NeedsReferenceUpload: len(req.References) > 0}
}

func accountLogLabel(account accounts.Account) string {
	if email := strings.TrimSpace(account.Email); email != "" {
		return email
	}
	if id := strings.TrimSpace(account.ID); id != "" {
		return id
	}
	return "unknown"
}

func copyProgressDetails(source map[string]any) map[string]any {
	if len(source) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(source)+8)
	for key, value := range source {
		out[key] = value
	}
	return out
}

func progressDetailInt(details map[string]any, key string) int {
	switch value := details[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	case string:
		var parsed int
		_, _ = fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed)
		return parsed
	default:
		return 0
	}
}

func progressDetailBool(details map[string]any, key string) bool {
	value, ok := details[key].(bool)
	return ok && value
}

func progressDetailString(details map[string]any, key string) string {
	if value, ok := details[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func progressDetailStringList(details map[string]any, key string) string {
	value := details[key]
	switch typed := value.(type) {
	case []string:
		return strings.Join(typed, ",")
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				values = append(values, strings.TrimSpace(text))
			}
		}
		return strings.Join(values, ",")
	default:
		return ""
	}
}

// instrumentImageRequest enriches task progress with the account lease and
// poll diagnostics. The account email is used only by the process log; public
// task details are sanitized by openaiweb.PublicDetails.
func (s *Service) instrumentImageRequest(req Request, lease accounts.ImageLease, account accounts.Account, attempt *openaiweb.AttemptLog) Request {
	upstreamProgress := req.Progress
	req.Progress = func(event openaiweb.ProgressEvent) {
		details := copyProgressDetails(event.Details)
		details["task_id"] = strings.TrimSpace(req.TaskID)
		details["lease_id"] = lease.ID
		details["account_id"] = strings.TrimSpace(account.ID)
		details["account_email"] = strings.TrimSpace(account.Email)
		active, effectiveLimit, configuredCeiling := 0, 0, 0
		if s != nil && s.store != nil {
			active, effectiveLimit, configuredCeiling = s.store.ImageAccountLeaseStats(account.AccessToken)
		}
		details["active_slots"] = active
		details["effective_limit"] = effectiveLimit
		details["configured_ceiling"] = configuredCeiling
		if attempt != nil {
			if conversationID := progressDetailString(details, "conversation_id"); conversationID != "" {
				attempt.ConversationID = conversationID
			}
			attempt.Phase = event.Progress
			attempt.PollCount = max(attempt.PollCount, progressDetailInt(details, "poll_count"))
			attempt.LastHTTPStatus = max(attempt.LastHTTPStatus, progressDetailInt(details, "last_http_status"))
			attempt.EmptyResultSecs = max(attempt.EmptyResultSecs, progressDetailInt(details, "empty_result_secs"))
			attempt.ToolSeen = attempt.ToolSeen || progressDetailBool(details, "tool_seen")
			attempt.ImageReferenceSeen = attempt.ImageReferenceSeen || progressDetailBool(details, "image_reference_seen")
			attempt.AssistantTextSeen = attempt.AssistantTextSeen || progressDetailBool(details, "assistant_text_seen")
			if role := progressDetailString(details, "last_role"); role != "" {
				attempt.LastRole = role
			}
			if signature := progressDetailString(details, "result_signature"); signature != "" {
				attempt.ResultSignature = signature
			}
			attempt.ActiveSlots = active
			attempt.EffectiveLimit = effectiveLimit
			attempt.ConfiguredCeiling = configuredCeiling
		}
		if event.Progress == "polling_image" {
			log.Printf("image_poll event=heartbeat task_id=%s attempt=%d lease_id=%s account=%s conversation_id=%s phase=%s poll_count=%d last_http_status=%d empty_result_secs=%d tool_seen=%t image_reference_seen=%t assistant_text_seen=%t result_signature=%s response_snapshots=%d response_bytes=%d response_fingerprint=%s response_roles=%s response_tools=%s response_statuses=%s response_references=%s response_candidate_keys=%s response_candidate_values=%d response_raw_file_refs=%d response_raw_sediment_refs=%d response_file_refs=%d response_sediment_refs=%d active_slots=%d effective_limit=%d configured_ceiling=%d", strings.TrimSpace(req.TaskID), attemptNumber(attempt), lease.ID, accountLogLabel(account), progressDetailString(details, "conversation_id"), event.Progress, progressDetailInt(details, "poll_count"), progressDetailInt(details, "last_http_status"), progressDetailInt(details, "empty_result_secs"), progressDetailBool(details, "tool_seen"), progressDetailBool(details, "image_reference_seen"), progressDetailBool(details, "assistant_text_seen"), progressDetailString(details, "result_signature"), progressDetailInt(details, "response_snapshots"), progressDetailInt(details, "response_bytes"), progressDetailString(details, "response_fingerprint"), progressDetailStringList(details, "response_roles"), progressDetailStringList(details, "response_tools"), progressDetailStringList(details, "response_statuses"), progressDetailStringList(details, "response_references"), progressDetailStringList(details, "response_candidate_keys"), progressDetailInt(details, "response_candidate_values"), progressDetailInt(details, "response_raw_file_refs"), progressDetailInt(details, "response_raw_sediment_refs"), progressDetailInt(details, "response_file_refs"), progressDetailInt(details, "response_sediment_refs"), active, effectiveLimit, configuredCeiling)
		}
		event.Details = details
		if upstreamProgress != nil {
			upstreamProgress(event)
		}
	}
	return req
}

func attemptNumber(attempt *openaiweb.AttemptLog) int {
	if attempt == nil {
		return 0
	}
	return attempt.Attempt
}

func logImageAttemptSwitch(req Request, lease accounts.ImageLease, account accounts.Account, attempt *openaiweb.AttemptLog, reason string) {
	if attempt == nil {
		return
	}
	attempt.SwitchReason = strings.TrimSpace(reason)
	log.Printf("image_attempt event=switch task_id=%s attempt=%d lease_id=%s account=%s conversation_id=%s reason=%s poll_count=%d last_http_status=%d empty_result_secs=%d tool_seen=%t image_reference_seen=%t result_signature=%s %s", strings.TrimSpace(req.TaskID), attempt.Attempt, lease.ID, accountLogLabel(account), attempt.ConversationID, attempt.SwitchReason, attempt.PollCount, attempt.LastHTTPStatus, attempt.EmptyResultSecs, attempt.ToolSeen, attempt.ImageReferenceSeen, attempt.ResultSignature, attempt.ResponseDiagnostics.LogFieldsWithAssistantText())
}

func (s *Service) generateOne(ctx context.Context, req Request) (openaiweb.ImageResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	exclude := map[string]bool{}
	attempts := []openaiweb.AttemptLog{}
	maxAttempts := s.currentConfig().MaxImageAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	var lastErr error
	imageAttempts := 0
	switches := 0
	authenticationRetries := 0
	var taskCtx context.Context
	var cancelTask context.CancelFunc
	defer func() {
		if cancelTask != nil {
			cancelTask()
		}
	}()
	for imageAttempts < maxAttempts {
		acquireCtx := ctx
		if taskCtx != nil {
			acquireCtx = taskCtx
		}
		lease, releaseGlobal, err := s.acquireImageDispatch(acquireCtx, exclude, imageDispatchRequirements(req), func() {
			reportAccountWait(req, accounts.Account{})
			if req.DispatchState != nil {
				req.DispatchState("waiting")
			}
		}, func() {
			if req.DispatchState != nil {
				req.DispatchState("acquiring")
			}
		})
		if err != nil {
			if lastErr != nil {
				return openaiweb.ImageResult{Attempts: attempts}, fmt.Errorf("%w; attempts=%v", lastErr, attempts)
			}
			return openaiweb.ImageResult{Attempts: attempts}, err
		}
		if taskCtx == nil {
			taskCtx, cancelTask = s.taskContext(ctx, req)
		}
		account := lease.Account
		exclude[account.AccessToken] = true
		attemptStarted := time.Now()
		attemptLog := openaiweb.AttemptLog{Attempt: len(attempts) + 1, AccountID: account.ID, AccountEmail: account.Email, LeaseID: lease.ID, Status: "running"}
		attemptLog.Phase = "preparing"
		activeSlots, effectiveLimit, configuredCeiling := s.store.ImageAccountLeaseStats(account.AccessToken)
		attemptLog.ActiveSlots = activeSlots
		attemptLog.EffectiveLimit = effectiveLimit
		attemptLog.ConfiguredCeiling = configuredCeiling
		log.Printf("image_attempt event=acquired task_id=%s attempt=%d lease_id=%s account=%s phase=preparing active_slots=%d effective_limit=%d configured_ceiling=%d", strings.TrimSpace(req.TaskID), attemptLog.Attempt, lease.ID, accountLogLabel(account), activeSlots, effectiveLimit, configuredCeiling)
		finishAttempt := func() openaiweb.AttemptLog {
			attemptLog.DurationMS = time.Since(attemptStarted).Milliseconds()
			attemptLog.ActiveSlots, attemptLog.EffectiveLimit, attemptLog.ConfiguredCeiling = s.store.ImageAccountLeaseStats(account.AccessToken)
			log.Printf("image_attempt event=finished task_id=%s attempt=%d lease_id=%s account=%s conversation_id=%s phase=%s status=%s duration_ms=%d poll_count=%d last_http_status=%d empty_result_secs=%d tool_seen=%t image_reference_seen=%t assistant_text_seen=%t result_signature=%s switch_reason=%s active_slots=%d effective_limit=%d configured_ceiling=%d removed_account=%t error=%s %s", strings.TrimSpace(req.TaskID), attemptLog.Attempt, lease.ID, accountLogLabel(account), attemptLog.ConversationID, attemptLog.Phase, attemptLog.Status, attemptLog.DurationMS, attemptLog.PollCount, attemptLog.LastHTTPStatus, attemptLog.EmptyResultSecs, attemptLog.ToolSeen, attemptLog.ImageReferenceSeen, attemptLog.AssistantTextSeen, attemptLog.ResultSignature, attemptLog.SwitchReason, attemptLog.ActiveSlots, attemptLog.EffectiveLimit, attemptLog.ConfiguredCeiling, attemptLog.RemovedAccount, compactImageAttemptError(attemptLog.Error), attemptLog.ResponseDiagnostics.LogFieldsWithAssistantText())
			return attemptLog
		}
		account, err = s.prepareAccountForDispatch(account, req)
		if err != nil {
			s.store.ReleaseImageLease(lease.ID)
			releaseGlobal()
			lastErr = err
			attemptLog.Status = "failed"
			attemptLog.Error = err.Error()
			if openaiweb.IsAuthenticationError(err) {
				removed, _ := s.store.RemoveInvalidToken(account.AccessToken, err.Error())
				attemptLog.RemovedAccount = removed
			}
			if openaiweb.IsNoFreeImageQuotaError(err) {
				removed, _ := s.store.RemoveQuotaExhausted(account.AccessToken, err)
				attemptLog.RemovedAccount = removed
			}
			attempts = append(attempts, finishAttempt())
			continue
		}
		attemptLog.AccountEmail = account.Email
		imageAttempts++
		attemptLog.Phase = "generating"
		attemptReq := s.instrumentImageRequest(req, lease, account, &attemptLog)
		leaseCtx, cancelLeaseContext := bindImageLeaseContext(taskCtx, lease)
		result, err := s.backend.GenerateImage(leaseCtx, account, attemptReq)
		cancelLeaseContext()
		applyAttemptDiagnostics(&attemptLog, result, err)
		if err == nil {
			_ = s.store.MarkImageSuccess(account.AccessToken)
			s.reportAccountIdentity(req, account.AccessToken)
			s.store.ReleaseImageLease(lease.ID)
			releaseGlobal()
			result, err = s.finalizeResult(taskCtx, account, result, req)
			if err != nil {
				lastErr = err
				attemptLog.Status = "failed"
				attemptLog.Error = err.Error()
				if openaiweb.IsAuthenticationError(err) {
					attemptLog.RemovedAccount = s.removeInvalidImageAccount(account.AccessToken, err)
					if authenticationRetries < maxAuthenticationRetries {
						authenticationRetries++
						if imageAttempts >= maxAttempts {
							maxAttempts++
						}
						attempts = append(attempts, finishAttempt())
						reportAuthenticationRetry(req, account, err, authenticationRetries)
						continue
					}
				}
				attempts = append(attempts, finishAttempt())
				result.Attempts = append(result.Attempts, attempts...)
				return result, err
			}
			attemptLog.Status = "success"
			attemptLog.BackendModel = result.BackendModel
			attemptLog.ConversationID = result.ConversationID
			attempts = append(attempts, finishAttempt())
			result.Attempts = append(result.Attempts, attempts...)
			return result, nil
		}
		lastErr = err
		attemptLog.Status = "failed"
		attemptLog.Error = err.Error()
		accountEvicted := accounts.IsImageAccountEvicted(lease.Context)
		referenceImageRequired := openaiweb.IsImageReferenceRequired(err)
		if !accountEvicted && !referenceImageRequired {
			s.recordImageFailure(account.AccessToken, err)
		}
		authenticationError := openaiweb.IsAuthenticationError(err)
		if authenticationError {
			attemptLog.RemovedAccount = s.removeInvalidImageAccount(account.AccessToken, err)
		} else if openaiweb.IsNoFreeImageQuotaError(err) {
			removed, _ := s.store.RemoveQuotaExhausted(account.AccessToken, err)
			attemptLog.RemovedAccount = removed
		}
		s.store.ReleaseImageLease(lease.ID)
		releaseGlobal()
		if referenceImageRequired {
			log.Printf("image_attempt event=input_error task_id=%s attempt=%d lease_id=%s account=%s reason=reference_image_required", strings.TrimSpace(req.TaskID), attemptLog.Attempt, lease.ID, accountLogLabel(account))
			attempts = append(attempts, finishAttempt())
			return openaiweb.ImageResult{Attempts: attempts}, err
		}
		if accountEvicted {
			logImageAttemptSwitch(req, lease, account, &attemptLog, "account_evicted")
			attempts = append(attempts, finishAttempt())
			if taskCtx.Err() == nil {
				continue
			}
		}
		if openaiweb.IsInteractiveChallengeError(err) {
			attempts = append(attempts, finishAttempt())
			return openaiweb.ImageResult{Attempts: attempts}, err
		}
		if authenticationError {
			logImageAttemptSwitch(req, lease, account, &attemptLog, "authentication_failed")
			if authenticationRetries >= maxAuthenticationRetries {
				attempts = append(attempts, finishAttempt())
				return openaiweb.ImageResult{Attempts: attempts}, err
			}
			authenticationRetries++
			if imageAttempts >= maxAttempts {
				maxAttempts++
			}
			reportAuthenticationRetry(req, account, err, authenticationRetries)
			attempts = append(attempts, finishAttempt())
			continue
		}
		if openaiweb.IsImageGenerationStalled(err) {
			switches++
			logImageAttemptSwitch(req, lease, account, &attemptLog, "generation_stalled")
			if switches > s.currentConfig().ImageMaxSwitchesPerTask {
				attempts = append(attempts, finishAttempt())
				return openaiweb.ImageResult{Attempts: attempts}, err
			}
		}
		if attemptLog.SwitchReason == "" && openaiweb.IsRetryableImageError(err) {
			logImageAttemptSwitch(req, lease, account, &attemptLog, "retryable_error")
		}
		attempts = append(attempts, finishAttempt())
		if !openaiweb.IsRetryableImageError(err) {
			return openaiweb.ImageResult{Attempts: attempts}, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("image generation failed")
	}
	return openaiweb.ImageResult{Attempts: attempts}, lastErr
}

// removeInvalidImageAccount immediately evicts an account after an upstream
// authentication failure. The caller owns releasing any active image lease
// and global slot; this helper only changes account-pool membership.
func (s *Service) removeInvalidImageAccount(token string, err error) bool {
	if s == nil || s.store == nil || !openaiweb.IsAuthenticationError(err) {
		return false
	}
	removed, removeErr := s.store.RemoveInvalidToken(token, err.Error())
	if removeErr != nil {
		log.Printf("remove account after image authentication failure: %v", removeErr)
	} else if removed {
		log.Printf("removed account after image authentication failure")
	}
	return removed
}

func applyAttemptDiagnostics(log *openaiweb.AttemptLog, result openaiweb.ImageResult, err error) {
	if log == nil {
		return
	}
	diagnostics := result.Diagnostics
	var stalled *openaiweb.ImageGenerationStalledError
	if errors.As(err, &stalled) {
		diagnostics = stalled.Diagnostics
		if log.SwitchReason == "" {
			log.SwitchReason = "generation_stalled"
		}
	}
	if diagnostics.Phase != "" {
		log.Phase = diagnostics.Phase
	}
	if diagnostics.PollCount > 0 {
		log.PollCount = diagnostics.PollCount
	}
	if diagnostics.LastHTTPStatus > 0 {
		log.LastHTTPStatus = diagnostics.LastHTTPStatus
	}
	if diagnostics.EmptyResultSecs > 0 {
		log.EmptyResultSecs = diagnostics.EmptyResultSecs
	}
	log.ToolSeen = log.ToolSeen || diagnostics.ToolSeen
	log.ImageReferenceSeen = log.ImageReferenceSeen || diagnostics.ImageReferenceSeen
	log.AssistantTextSeen = log.AssistantTextSeen || diagnostics.AssistantTextSeen
	if diagnostics.LastRole != "" {
		log.LastRole = diagnostics.LastRole
	}
	if diagnostics.ResultSignature != "" {
		log.ResultSignature = diagnostics.ResultSignature
	}
	log.ResponseDiagnostics = diagnostics.Response
}

func compactImageAttemptError(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 240 {
		return message[:240]
	}
	return message
}

// recordImageFailure applies dispatch backoff once for each failed attempt.
// Authentication and quota failures retain their existing caller-specific
// handling below; temporary upstream failures instead cool the account so
// parallel tasks do not immediately select it again.
func (s *Service) recordImageFailure(token string, err error) {
	if s == nil || s.store == nil || err == nil || openaiweb.IsInteractiveChallengeError(err) {
		return
	}
	// Request/content results do not describe account health. In particular,
	// an upstream request for a missing reference image must not increase the
	// account's abnormal counter when the caller uses a pinned account path.
	if errors.Is(err, openaiweb.ErrContentPolicy) || openaiweb.IsImageReferenceRequired(err) || (errors.Is(err, openaiweb.ErrImageAssistantText) && !openaiweb.IsSkippedMainlineImageError(err)) {
		return
	}
	// A full Turnstile VM pool is process-wide congestion rather than a
	// failure of this account. Do not cool or mark every waiting account.
	if errors.Is(err, openaiweb.ErrTurnstileVMCapacity) {
		return
	}
	if openaiweb.IsImageReferenceUploadRateLimited(err) {
		retryAfter := time.Duration(0)
		var upstream *openaiweb.UpstreamError
		if errors.As(err, &upstream) && upstream.RetryAfter > 0 {
			retryAfter = time.Duration(upstream.RetryAfter) * time.Second
		}
		_ = s.store.MarkImageReferenceUploadRateLimited(token, retryAfter, err)
		return
	}
	if openaiweb.IsAuthenticationError(err) || openaiweb.IsNoFreeImageQuotaError(err) {
		_ = s.store.MarkFailure(token, err)
		return
	}
	if openaiweb.IsImageGenerationStalled(err) {
		s.stalled.Add(1)
		_ = s.store.MarkImageStalled(token, err)
		return
	}
	if openaiweb.IsImageConversationTimeout(err) {
		_ = s.store.MarkFailure(token, err)
		return
	}
	if errors.Is(err, openaiweb.ErrImageGenerationTerminated) {
		_ = s.store.MarkImageGenerationTerminated(token, err)
		return
	}
	var upstream *openaiweb.UpstreamError
	if errors.As(err, &upstream) && (upstream.StatusCode == http.StatusTooManyRequests || upstream.StatusCode >= http.StatusInternalServerError) {
		retryAfter := time.Duration(max(0, upstream.RetryAfter)) * time.Second
		_ = s.store.MarkImageHTTPFailure(token, upstream.StatusCode, retryAfter, err)
		return
	}
	if errors.Is(err, openaiweb.ErrPollTimeout) || openaiweb.IsRetryableImageError(err) {
		_ = s.store.MarkImageUpstreamFailure(token, err)
		return
	}
	_ = s.store.MarkFailure(token, err)
}

// prepareAccountForDispatch performs only local bookkeeping. GenerateImage is
// the authoritative token/Sentinel check, so normal dispatch does not repeat
// the same upstream bootstrap immediately before generation.
func (s *Service) prepareAccountForDispatch(account accounts.Account, req Request) (accounts.Account, error) {
	var err error
	account, err = s.ensureBrowserIdentity(account)
	if err != nil {
		return account, err
	}
	if strings.TrimSpace(account.AccessToken) == "" {
		return account, fmt.Errorf("access token is required")
	}
	reportAccountProgress(req, "account_ready", "开始生图", account)
	return account, nil
}

func reportAccountProgress(req Request, progress, message string, account accounts.Account) {
	reportAccountIdentity(req, account)
	if req.Progress == nil {
		return
	}
	req.Progress(openaiweb.ProgressEvent{Progress: progress, Message: message})
}

func reportAccountIdentity(req Request, account accounts.Account) {
	if req.AccountProgress == nil || (strings.TrimSpace(account.ID) == "" && strings.TrimSpace(account.Email) == "") {
		return
	}
	var availableQuota *int
	if !account.ImageQuotaUnknown {
		quota := max(0, account.Quota)
		availableQuota = &quota
	}
	req.AccountProgress(openaiweb.AccountIdentity{ID: strings.TrimSpace(account.ID), Email: strings.TrimSpace(account.Email), AvailableQuota: availableQuota})
}

func (s *Service) reportAccountIdentity(req Request, token string) {
	if s == nil || s.store == nil || req.AccountProgress == nil {
		return
	}
	account, found := s.store.Get(token)
	if found {
		reportAccountIdentity(req, account)
	}
}

func reportAccountWait(req Request, account accounts.Account) {
	reportAccountProgress(req, "waiting_account", "当前处理资源繁忙，任务排队等待", account)
}

func reportAuthenticationRetry(req Request, account accounts.Account, err error, retry int) {
	if req.Progress == nil {
		return
	}
	req.Progress(openaiweb.ProgressEvent{
		Progress: "retrying_account",
		Message:  openaiweb.PublicCredentialInvalidMessage,
	})
}

type imageDownloader interface {
	DownloadImageFor(ctx context.Context, account accounts.Account, imageURL string) ([]byte, error)
}

func normalizeResponseFormat(value string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(value))
	switch format {
	case "":
		return "b64_json", nil
	case "url":
		return "url", nil
	case "b64_json":
		return "b64_json", nil
	default:
		return "", fmt.Errorf("invalid response_format %q; supported values are url, b64_json", value)
	}
}

func normalizeOutputFormat(value string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(value))
	format = strings.TrimPrefix(format, ".")
	switch format {
	case "", "auto":
		return "", nil
	case "jpg":
		return "jpeg", nil
	case "png", "jpeg", "webp":
		return format, nil
	default:
		return "", fmt.Errorf("invalid output_format %q; supported values are png, jpeg, webp", value)
	}
}

func (s *Service) finalizeResult(ctx context.Context, account accounts.Account, result openaiweb.ImageResult, req Request) (openaiweb.ImageResult, error) {
	responseFormat, err := normalizeResponseFormat(req.ResponseFormat)
	if err != nil {
		return result, err
	}
	outputFormat, err := normalizeOutputFormat(req.OutputFormat)
	if err != nil {
		return result, err
	}
	if responseFormat == "b64_json" {
		return s.resultAsBase64(ctx, account, result, req, outputFormat)
	}
	return s.cacheResult(ctx, account, result, req, outputFormat)
}

func (s *Service) resultAsBase64(ctx context.Context, account accounts.Account, result openaiweb.ImageResult, req Request, outputFormat string) (openaiweb.ImageResult, error) {
	dataItems, err := s.resultImageBytes(ctx, account, result)
	if err != nil {
		return result, err
	}
	out := result
	out.URLs = nil
	out.B64JSON = make([]string, 0, len(dataItems))
	for _, data := range dataItems {
		if outputFormat != "" {
			var err error
			data, err = convertImageDataFormat(data, outputFormat)
			if err != nil {
				return result, err
			}
		}
		out.B64JSON = append(out.B64JSON, base64.StdEncoding.EncodeToString(data))
	}
	return out, nil
}

func (s *Service) resultImageBytes(ctx context.Context, account accounts.Account, result openaiweb.ImageResult) ([][]byte, error) {
	items := make([][]byte, 0, len(result.B64JSON)+len(result.URLs))
	for _, encoded := range result.B64JSON {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode b64_json image: %w", err)
		}
		items = append(items, data)
	}
	if len(result.URLs) == 0 {
		if len(items) == 0 {
			return nil, fmt.Errorf("upstream completed without generating images")
		}
		return items, nil
	}
	downloader, ok := s.backend.(imageDownloader)
	if !ok {
		return nil, fmt.Errorf("image downloader is required for response_format=b64_json")
	}
	for _, remoteURL := range result.URLs {
		data, err := downloader.DownloadImageFor(ctx, account, remoteURL)
		if err != nil {
			return nil, err
		}
		items = append(items, data)
	}
	return items, nil
}

func (s *Service) cacheResult(ctx context.Context, account accounts.Account, result openaiweb.ImageResult, req Request, outputFormat string) (openaiweb.ImageResult, error) {
	if s.storage == nil {
		return result, nil
	}
	if len(result.URLs) == 0 && len(result.B64JSON) == 0 {
		return result, nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(req.OutputBaseURL), "/")
	urls := make([]string, 0, len(result.URLs)+len(result.B64JSON))
	for _, encoded := range result.B64JSON {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			log.Printf("image cache decode b64_json failed: %v", err)
			continue
		}
		if outputFormat != "" {
			data, err = convertImageDataFormat(data, outputFormat)
			if err != nil {
				log.Printf("image cache convert b64_json to %s failed: %v", outputFormat, err)
				continue
			}
		}
		item, err := s.storage.Save(data)
		if err != nil {
			log.Printf("image cache save failed: %v", err)
			continue
		}
		urls = append(urls, imageURL(baseURL, item.Rel))
	}
	if len(result.URLs) == 0 {
		result.URLs = urls
		result.B64JSON = nil
		return result, nil
	}
	downloader, ok := s.backend.(imageDownloader)
	if !ok {
		return result, nil
	}
	for _, remoteURL := range result.URLs {
		data, err := downloader.DownloadImageFor(ctx, account, remoteURL)
		if err != nil {
			log.Printf("image cache download failed: %v", err)
			if isCacheDownloadAuthenticationFailure(err) {
				return result, err
			}
			urls = append(urls, remoteURL)
			continue
		}
		if outputFormat != "" {
			data, err = convertImageDataFormat(data, outputFormat)
			if err != nil {
				log.Printf("image cache convert download to %s failed: %v", outputFormat, err)
				urls = append(urls, remoteURL)
				continue
			}
		}
		item, err := s.storage.Save(data)
		if err != nil {
			log.Printf("image cache save failed: %v", err)
			urls = append(urls, remoteURL)
			continue
		}
		urls = append(urls, imageURL(baseURL, item.Rel))
	}
	result.URLs = urls
	result.B64JSON = nil
	return result, nil
}

func imageURL(baseURL, rel string) string {
	encoded := url.PathEscape(rel)
	if baseURL == "" {
		return "/images/" + encoded
	}
	return strings.TrimRight(baseURL, "/") + "/images/" + encoded
}

func convertImageDataFormat(data []byte, outputFormat string) ([]byte, error) {
	outputFormat, err := normalizeOutputFormat(outputFormat)
	if err != nil {
		return nil, err
	}
	if outputFormat == "" {
		return data, nil
	}
	current := imageFormatFromMIMEType(imageMIMETypeFromBytes(data))
	if current == outputFormat {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image for %s conversion: %w", outputFormat, err)
	}
	buffer := new(bytes.Buffer)
	switch outputFormat {
	case "png":
		if err := png.Encode(buffer, img); err != nil {
			return nil, fmt.Errorf("encode png: %w", err)
		}
	case "jpeg":
		if err := jpeg.Encode(buffer, flattenForJPEG(img), &jpeg.Options{Quality: 95}); err != nil {
			return nil, fmt.Errorf("encode jpeg: %w", err)
		}
	case "webp":
		options := webp.DefaultOptions()
		options.Quality = 95
		options.Method = 4
		options.AlphaQuality = 100
		if err := webp.Encode(buffer, img, options); err != nil {
			return nil, fmt.Errorf("encode webp: %w", err)
		}
	default:
		return nil, fmt.Errorf("invalid output_format %q; supported values are png, jpeg, webp", outputFormat)
	}
	return buffer.Bytes(), nil
}

func flattenForJPEG(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Over)
	return dst
}

// Cache downloads normally use short-lived external URLs. Only a token
// revocation or a 401 from an account-scoped upstream request proves that the
// account itself is invalid, so an unrelated expired asset URL cannot evict a
// healthy account.
func isCacheDownloadAuthenticationFailure(err error) bool {
	if openaiweb.IsTokenInvalidError(err) {
		return true
	}
	var upstream *openaiweb.UpstreamError
	return errors.As(err, &upstream) && upstream.StatusCode == http.StatusUnauthorized
}

func (s *Service) ListModels(ctx context.Context) ([]string, error) {
	base := []string{PublicImageModel}
	account, err := s.store.SelectForImage(nil)
	if err != nil {
		return ExpandPublicModels(base), nil
	}
	account, err = s.ensureBrowserIdentity(account)
	if err != nil {
		return ExpandPublicModels(base), nil
	}
	var upstream []string
	if backend, ok := s.backend.(accountModelsForBackend); ok {
		upstream, err = backend.ListModelsFor(ctx, account)
	} else {
		upstream, err = s.backend.ListModels(ctx, account.AccessToken)
	}
	if err != nil {
		return ExpandPublicModels(base), nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, list := range [][]string{upstream, base} {
		for _, model := range list {
			if model == PublicImageModel && !seen[model] {
				seen[model] = true
				out = append(out, model)
			}
		}
	}
	return ExpandPublicModels(out), nil
}

func responseWithModel(response Response, model string) Response {
	model = PublicModelName(model)
	response.BackendModel = model
	if response.ImageRoute != nil {
		route := make(map[string]any, len(response.ImageRoute))
		for key, value := range response.ImageRoute {
			route[key] = value
		}
		route["backend_model"] = model
		response.ImageRoute = route
	}
	return response
}

func responseFromResult(result openaiweb.ImageResult) Response {
	resp := Response{Created: time.Now().Unix(), AccountEmail: result.AccountEmail, ConversationID: result.ConversationID, BackendModel: result.BackendModel, Attempts: result.Attempts}
	for _, url := range result.URLs {
		mimeType := imageMIMETypeFromURL(url)
		resp.Data = append(resp.Data, Data{URL: url, MimeType: mimeType, Format: imageFormatFromMIMEType(mimeType)})
	}
	for _, b64 := range result.B64JSON {
		mimeType := imageMIMETypeFromBase64(b64)
		resp.Data = append(resp.Data, Data{B64JSON: b64, MimeType: mimeType, Format: imageFormatFromMIMEType(mimeType)})
	}
	resp.ImageRoute = map[string]any{"backend_model": result.BackendModel, "image_route": "free_image2_fallback"}
	return resp
}

// Public removes account-specific upstream model slugs from an image response.
// It also normalizes persisted legacy responses when they are read back.
func (r Response) Public() Response {
	publicModel := PublicModelName(r.BackendModel)
	r.AccountEmail = ""
	r.BackendModel = publicModel
	r.Attempts = nil
	if r.ImageRoute != nil {
		route := make(map[string]any, len(r.ImageRoute))
		for key, value := range r.ImageRoute {
			route[key] = value
		}
		route["backend_model"] = publicModel
		r.ImageRoute = route
	}
	return r
}

func imageMIMETypeFromBase64(encoded string) string {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return imageMIMETypeFromBytes(data)
}

func imageMIMETypeFromBytes(data []byte) string {
	switch {
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 8 && data[0] == 0x89 && string(data[1:4]) == "PNG" && data[4] == 0x0d && data[5] == 0x0a && data[6] == 0x1a && data[7] == 0x0a:
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	default:
		return ""
	}
}

func imageMIMETypeFromURL(raw string) string {
	path := strings.ToLower(strings.TrimSpace(raw))
	if parsed, err := url.Parse(raw); err == nil {
		path = strings.ToLower(parsed.Path)
	}
	switch {
	case strings.HasSuffix(path, ".png"):
		return "image/png"
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(path, ".webp"):
		return "image/webp"
	case strings.HasSuffix(path, ".gif"):
		return "image/gif"
	default:
		return ""
	}
}

func imageFormatFromMIMEType(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpeg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return ""
	}
}

func (r Response) MarshalForOpenAI() map[string]any {
	r = r.Public()
	data, _ := json.Marshal(r)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
