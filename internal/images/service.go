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
	"time"

	"github.com/deepteams/webp"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/openaiweb"
	"imagepool/internal/storage"
)

const maxAuthenticationRetries = 10

type Service struct {
	cfgMu   sync.RWMutex
	cfg     config.Config
	store   *accounts.Store
	backend openaiweb.Backend
	storage *storage.Service
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
	AccountEmail   string                 `json:"account_email,omitempty"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	BackendModel   string                 `json:"backend_model,omitempty"`
	Attempts       []openaiweb.AttemptLog `json:"attempts,omitempty"`
	ImageRoute     map[string]any         `json:"image_route,omitempty"`
}

func NewService(cfg config.Config, store *accounts.Store, backend openaiweb.Backend, imageStorage ...*storage.Service) *Service {
	cfg = cfg.Normalize()
	service := &Service{cfg: cfg, store: store, backend: backend}
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
}

func (s *Service) currentConfig() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Service) Generate(ctx context.Context, req Request) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
		if err != nil {
			return responseFromResult(result), err
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
	taskCtx, cancel := s.taskContext(ctx)
	defer cancel()
	released := false
	release := func() {
		if released {
			return
		}
		s.store.ReleaseImage(account.AccessToken)
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
	result, err := s.backend.GenerateImage(taskCtx, account, req)
	if err != nil {
		s.recordImageFailure(account.AccessToken, err)
		if openaiweb.IsAuthenticationError(err) {
			_, _ = s.store.RemoveInvalidToken(account.AccessToken, err.Error())
		} else if openaiweb.IsNoFreeImageQuotaError(err) {
			_ = s.store.MarkImageQuotaExhausted(account.AccessToken, err)
		}
		return Response{}, err
	}
	_ = s.store.MarkImageSuccess(account.AccessToken)
	// Downloads only need the immutable account identity; releasing the image
	// lease here lets the next queued generation start while the local cache is
	// populated.
	release()
	result, err = s.finalizeResult(taskCtx, account, result, req)
	if err != nil {
		return responseFromResult(result), err
	}
	return responseFromResult(result), nil
}

func (s *Service) taskContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	timeout := time.Duration(s.currentConfig().ImageTaskTimeoutSecs * float64(time.Second))
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
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
		account, err := s.store.AcquireForImage(acquireCtx, exclude, func() {
			reportAccountWait(req, accounts.Account{})
		})
		if err != nil {
			if lastErr != nil {
				return openaiweb.ImageResult{Attempts: attempts}, fmt.Errorf("%w; attempts=%v", lastErr, attempts)
			}
			return openaiweb.ImageResult{Attempts: attempts}, err
		}
		exclude[account.AccessToken] = true
		if taskCtx == nil {
			taskCtx, cancelTask = s.taskContext(ctx)
		}
		log := openaiweb.AttemptLog{Attempt: len(attempts) + 1, AccountEmail: account.Email, Status: "running"}
		account, err = s.prepareAccountForDispatch(account, req)
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
			continue
		}
		log.AccountEmail = account.Email
		imageAttempts++
		result, err := s.backend.GenerateImage(taskCtx, account, req)
		if err == nil {
			_ = s.store.MarkImageSuccess(account.AccessToken)
			s.store.ReleaseImage(account.AccessToken)
			result, err = s.finalizeResult(taskCtx, account, result, req)
			if err != nil {
				log.Status = "failed"
				log.Error = err.Error()
				attempts = append(attempts, log)
				result.Attempts = append(result.Attempts, attempts...)
				return result, err
			}
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
		s.recordImageFailure(account.AccessToken, err)
		authenticationError := openaiweb.IsAuthenticationError(err)
		if authenticationError {
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
		if authenticationError {
			if authenticationRetries >= maxAuthenticationRetries {
				return openaiweb.ImageResult{Attempts: attempts}, err
			}
			authenticationRetries++
			if imageAttempts >= maxAttempts {
				maxAttempts++
			}
			reportAuthenticationRetry(req, account, err, authenticationRetries)
			continue
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

// recordImageFailure applies dispatch backoff once for each failed attempt.
// Authentication and quota failures retain their existing caller-specific
// handling below; temporary upstream failures instead cool the account so
// parallel tasks do not immediately select it again.
func (s *Service) recordImageFailure(token string, err error) {
	if s == nil || s.store == nil || err == nil || openaiweb.IsInteractiveChallengeError(err) {
		return
	}
	// A full Turnstile VM pool is process-wide congestion rather than a
	// failure of this account. Do not cool or mark every waiting account.
	if errors.Is(err, openaiweb.ErrTurnstileVMCapacity) {
		return
	}
	if openaiweb.IsAuthenticationError(err) || openaiweb.IsNoFreeImageQuotaError(err) {
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
	reportAccountProgress(req, "account_ready", "账号已分配，开始生图", account)
	return account, nil
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

func reportAuthenticationRetry(req Request, account accounts.Account, err error, retry int) {
	if req.Progress == nil {
		return
	}
	details := map[string]any{"retry": retry, "max_retries": maxAuthenticationRetries, "error": openaiweb.PublicErrorMessage(err)}
	if account.Email != "" {
		details["account_email"] = account.Email
	}
	req.Progress(openaiweb.ProgressEvent{
		Progress: "retrying_account",
		Message:  fmt.Sprintf("账号凭证失效，已删除账号并切换账号重试（%d/%d）", retry, maxAuthenticationRetries),
		Details:  details,
	})
}

type imageDownloader interface {
	DownloadImageFor(ctx context.Context, account accounts.Account, imageURL string) ([]byte, error)
}

func normalizeResponseFormat(value string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(value))
	switch format {
	case "", "url":
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
		return s.resultAsBase64(ctx, account, result, outputFormat)
	}
	return s.cacheResult(ctx, account, result, req.OutputBaseURL, outputFormat)
}

func (s *Service) resultAsBase64(ctx context.Context, account accounts.Account, result openaiweb.ImageResult, outputFormat string) (openaiweb.ImageResult, error) {
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

func (s *Service) cacheResult(ctx context.Context, account accounts.Account, result openaiweb.ImageResult, baseURL string, outputFormat string) (openaiweb.ImageResult, error) {
	if s.storage == nil {
		return result, nil
	}
	if len(result.URLs) == 0 && len(result.B64JSON) == 0 {
		return result, nil
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	urls := make([]string, 0, len(result.URLs)+len(result.B64JSON))
	credentialInvalid := false
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
		if credentialInvalid {
			urls = append(urls, remoteURL)
			continue
		}
		data, err := downloader.DownloadImageFor(ctx, account, remoteURL)
		if err != nil {
			log.Printf("image cache download failed: %v", err)
			if isCacheDownloadAuthenticationFailure(err) {
				credentialInvalid = true
				removed, removeErr := s.store.RemoveInvalidToken(account.AccessToken, err.Error())
				if removeErr != nil {
					log.Printf("remove account after image cache authentication failure: %v", removeErr)
				} else if removed {
					log.Printf("removed account after image cache authentication failure")
				}
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
	r.Attempts = openaiweb.PublicAttemptLogs(r.Attempts)
	data, _ := json.Marshal(r)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
