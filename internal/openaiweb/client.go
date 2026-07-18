package openaiweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/browsertransport"
	"imagepool/internal/config"
	"imagepool/internal/proxy"
)

const (
	defaultUserAgent         = accounts.DefaultBrowserUserAgent
	defaultClientVersion     = "prod-de97061a1c9aff3931a7342defd6241031cd316a"
	defaultClientBuildNumber = "8160987"
	bootstrapResourcesTTL    = 5 * time.Minute
	// Keep setup failures from consuming the full image-generation window. A
	// submitted image still receives the complete poll timeout below.
	defaultImagePreparationTimeout = 30 * time.Second
)

type bootstrapResourcesCacheKey struct {
	accessToken string
	proxy       string
}

type bootstrapResourcesCacheEntry struct {
	scripts   []string
	build     string
	expiresAt time.Time
}

type bootstrapResourcesFlight struct {
	done    chan struct{}
	scripts []string
	build   string
	err     error
}

type Client struct {
	baseURL                     string
	imageModelSlug              string
	pollTimeout                 time.Duration
	pollInterval                time.Duration
	pollInitialWait             time.Duration
	pollHeartbeatInterval       time.Duration
	pollRequestTimeout          time.Duration
	imagePreparationTimeout     time.Duration
	imageStreamOpenTimeout      time.Duration
	imageStreamIdleTimeout      time.Duration
	imageStreamReferenceTimeout time.Duration
	settle                      time.Duration
	checkBeforeHit              bool
	settleEnabled               bool
	httpClient                  *http.Client
	resourceClient              *http.Client
	proxyRuntime                config.ProxyRuntime
	transport                   string
	timeout                     time.Duration
	customHTTP                  bool
	tlsMu                       sync.Mutex
	tlsClients                  map[string]*http.Client
	tlsResources                map[string]*http.Client
	tlsStreamClients            map[string]*http.Client
	bootstrapMu                 sync.Mutex
	bootstrapResources          map[bootstrapResourcesCacheKey]bootstrapResourcesCacheEntry
	bootstrapFlights            map[bootstrapResourcesCacheKey]*bootstrapResourcesFlight
	now                         func() time.Time
	newID                       func() string
	sleep                       func(context.Context, time.Duration) error
	bootstrapClearanceRefresh   func(context.Context) (*Client, error)
}

type ClientOption func(*Client)

func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = h
		c.resourceClient = h
		c.customHTTP = true
	}
}
func WithIDGenerator(fn func() string) ClientOption { return func(c *Client) { c.newID = fn } }
func WithSleep(fn func(context.Context, time.Duration) error) ClientOption {
	return func(c *Client) { c.sleep = fn }
}

func withBootstrapClearanceRefresh(fn func(context.Context) (*Client, error)) ClientOption {
	return func(c *Client) { c.bootstrapClearanceRefresh = fn }
}

func NewClient(cfg config.Config, opts ...ClientOption) *Client {
	cfg = cfg.Normalize()
	httpClient, err := upstreamHTTPClient(cfg, false)
	if err != nil {
		httpClient = &http.Client{Timeout: seconds(cfg.RequestTimeoutSecs)}
	}
	resourceClient, err := upstreamHTTPClient(cfg, true)
	if err != nil {
		resourceClient = httpClient
	}
	c := &Client{
		baseURL: strings.TrimRight(cfg.ChatGPTBaseURL, "/"), imageModelSlug: cfg.ImageWebModelSlug,
		pollTimeout: seconds(cfg.ImagePollTimeoutSecs), pollInterval: seconds(cfg.ImagePollIntervalSecs), pollInitialWait: seconds(cfg.ImagePollInitialWaitSecs), pollHeartbeatInterval: 15 * time.Second, pollRequestTimeout: 20 * time.Second, imagePreparationTimeout: defaultImagePreparationTimeout, imageStreamOpenTimeout: defaultImageStreamOpenTimeout, imageStreamIdleTimeout: defaultImageStreamIdleWindow, imageStreamReferenceTimeout: defaultImageStreamReferenceWindow, settle: seconds(cfg.ImageSettleSecs),
		checkBeforeHit: cfg.ImageCheckBeforeHitEnabled, settleEnabled: cfg.ImageSettleEnabled,
		httpClient: httpClient, resourceClient: resourceClient, proxyRuntime: cfg.ProxyRuntime, transport: cfg.UpstreamTransport, timeout: seconds(cfg.RequestTimeoutSecs), tlsClients: map[string]*http.Client{}, tlsResources: map[string]*http.Client{}, tlsStreamClients: map[string]*http.Client{}, bootstrapResources: map[bootstrapResourcesCacheKey]bootstrapResourcesCacheEntry{}, bootstrapFlights: map[bootstrapResourcesCacheKey]*bootstrapResourcesFlight{}, now: time.Now, newID: newUUID,
		sleep: func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				return nil
			}
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func upstreamHTTPClient(cfg config.Config, resource bool) (*http.Client, error) {
	timeout := seconds(cfg.RequestTimeoutSecs)
	if cfg.UpstreamTransport == "tls_client" {
		return browsertransport.NewHTTPClient(cfg.ProxyRuntime, timeout, resource)
	}
	if resource {
		return proxy.NewResourceHTTPClientForRuntime(cfg.ProxyRuntime, timeout)
	}
	return proxy.NewHTTPClient(cfg)
}

func seconds(v float64) time.Duration {
	if v <= 0 {
		return 0
	}
	return time.Duration(v * float64(time.Second))
}

type chatRequirements struct{ Token, PrepareToken, ProofToken, TurnstileToken, SOToken string }
type uploadMeta struct {
	FileID, FileName string
	FileSize         int
	MIMEType         string
	Width, Height    int
}

func (c *Client) ListModels(ctx context.Context, token string) ([]string, error) {
	return c.ListModelsFor(ctx, accounts.Account{AccessToken: strings.TrimSpace(token)})
}

func (c *Client) ListModelsFor(ctx context.Context, account accounts.Account) ([]string, error) {
	token := strings.TrimSpace(account.AccessToken)
	account.AccessToken = token
	if err := c.bootstrap(ctx, account); err != nil {
		return nil, err
	}
	path := "/backend-api/models?history_and_training_disabled=false"
	route := "/backend-api/models"
	if token == "" {
		path = "/backend-anon/models?iim=false&is_gizmo=false"
		route = "/backend-anon/models"
	}
	var payload struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := c.doJSON(ctx, account, http.MethodGet, path, route, nil, map[string]string{"Accept": "application/json"}, &payload); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, m := range payload.Models {
		slug := strings.TrimSpace(m.Slug)
		if slug != "" && !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	return out, nil
}

func (c *Client) GetAccountInfo(ctx context.Context, token string) (AccountInfo, error) {
	return c.GetAccountInfoFor(ctx, accounts.Account{AccessToken: strings.TrimSpace(token)})
}

func (c *Client) GetAccountInfoFor(ctx context.Context, account accounts.Account) (AccountInfo, error) {
	account.AccessToken = strings.TrimSpace(account.AccessToken)
	if account.AccessToken == "" {
		return AccountInfo{}, fmt.Errorf("access token is required")
	}
	if err := c.bootstrap(ctx, account); err != nil {
		return AccountInfo{}, err
	}
	var me struct {
		Email string `json:"email"`
	}
	if err := c.doJSON(ctx, account, http.MethodGet, "/backend-api/me", "/backend-api/me", nil, map[string]string{"Accept": "application/json"}, &me); err != nil {
		return AccountInfo{}, err
	}
	var initPayload struct {
		LimitsProgress   []map[string]any `json:"limits_progress"`
		DefaultModelSlug string           `json:"default_model_slug"`
	}
	initRequest := map[string]any{"gizmo_id": nil, "requested_default_model": nil, "conversation_id": nil, "timezone_offset_min": -480}
	if err := c.doJSON(ctx, account, http.MethodPost, "/backend-api/conversation/init", "/backend-api/conversation/init", initRequest, map[string]string{"Content-Type": "application/json"}, &initPayload); err != nil {
		return AccountInfo{}, err
	}
	var accountsPayload struct {
		Accounts struct {
			Default struct {
				Account struct {
					PlanType string `json:"plan_type"`
				} `json:"account"`
			} `json:"default"`
		} `json:"accounts"`
	}
	if err := c.doJSON(ctx, account, http.MethodGet, "/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-480", "/backend-api/accounts/check/v4-2023-04-27", nil, map[string]string{"Accept": "application/json"}, &accountsPayload); err != nil {
		return AccountInfo{}, err
	}
	info := AccountInfo{Email: strings.TrimSpace(me.Email), Type: strings.TrimSpace(accountsPayload.Accounts.Default.Account.PlanType), ImageQuotaUnknown: true, LimitsProgress: initPayload.LimitsProgress, DefaultModelSlug: strings.TrimSpace(initPayload.DefaultModelSlug)}
	if info.Type == "" {
		info.Type = "free"
	}
	for _, limit := range info.LimitsProgress {
		if strings.TrimSpace(fmt.Sprint(limit["feature_name"])) != "image_gen" {
			continue
		}
		info.Quota = intValue(limit["remaining"])
		info.RestoreAt = strings.TrimSpace(fmt.Sprint(limit["reset_after"]))
		info.ImageQuotaUnknown = false
		break
	}
	return info, nil
}

// CheckImageReady verifies the token can complete the Sentinel handshake used
// by image generation without creating a conversation or submitting an image.
func (c *Client) CheckImageReady(ctx context.Context, token string) error {
	return c.CheckImageReadyFor(ctx, accounts.Account{AccessToken: strings.TrimSpace(token)})
}

func (c *Client) CheckImageReadyFor(ctx context.Context, account accounts.Account) error {
	account.AccessToken = strings.TrimSpace(account.AccessToken)
	if account.AccessToken == "" {
		return fmt.Errorf("access token is required")
	}
	scripts, dataBuild, err := c.bootstrapWithResources(ctx, account)
	if err != nil {
		return err
	}
	_, err = c.chatRequirements(ctx, account, scripts, dataBuild)
	return err
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	case string:
		result, _ := strconv.Atoi(strings.TrimSpace(typed))
		return result
	default:
		return 0
	}
}

func (c *Client) GenerateImage(ctx context.Context, account accounts.Account, req ImageRequest) (ImageResult, error) {
	if strings.TrimSpace(account.AccessToken) == "" {
		return ImageResult{}, fmt.Errorf("access token is required for image endpoints")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return ImageResult{}, fmt.Errorf("prompt is required")
	}
	progress := req.Progress
	if progress == nil {
		progress = func(ProgressEvent) {}
	}
	backendModel := c.imageSlug(req.Model)
	attemptCtx, cancelAttempt := c.imageAttemptContext(ctx)
	defer cancelAttempt()
	preparationCtx, cancelPreparation := c.imagePreparationContext(attemptCtx)
	defer cancelPreparation()

	progress(ProgressEvent{Progress: "uploading", Message: "上传参考图"})
	refs := make([]uploadMeta, 0, len(req.References))
	for i, image := range req.References {
		meta, err := c.uploadImage(preparationCtx, account, image, i+1, len(req.References))
		if err != nil {
			return ImageResult{}, c.imagePreparationError(ctx, err)
		}
		refs = append(refs, meta)
	}
	effectivePrompt := imagePromptForWeb(req.Prompt, len(refs) > 0, req.Size, req.Quality)
	progress(ProgressEvent{Progress: "bootstrapping", Message: "初始化 ChatGPT Web 会话"})
	scripts, dataBuild, err := c.bootstrapWithResources(preparationCtx, account)
	if err != nil {
		return ImageResult{}, c.imagePreparationError(ctx, err)
	}
	progress(ProgressEvent{Progress: "getting_token", Message: "获取 sentinel token"})
	requirements, err := c.chatRequirements(preparationCtx, account, scripts, dataBuild)
	if err != nil {
		return ImageResult{}, c.imagePreparationError(ctx, err)
	}
	progress(ProgressEvent{Progress: "preparing_conversation", Message: "准备生图会话"})
	conduit, turnTraceID, parentMessageID, err := c.prepareImageConversation(preparationCtx, account, effectivePrompt, backendModel, requirements, refs)
	if err != nil {
		return ImageResult{}, c.imagePreparationError(ctx, err)
	}
	// The preparation deadline must not continue to constrain a conversation
	// after it has been accepted by the upstream.
	cancelPreparation()
	progress(ProgressEvent{Progress: "starting_generation", Message: "提交生图请求"})
	generationCtx, cancelGeneration := c.imageGenerationContext(attemptCtx)
	defer cancelGeneration()
	conversationID, fileIDs, sedimentIDs, err := c.startImageGenerationWithinBudget(generationCtx, account, effectivePrompt, backendModel, requirements, conduit, turnTraceID, parentMessageID, refs)
	if err != nil {
		return ImageResult{}, imageAttemptError(ctx, generationCtx, err)
	}
	progress(ProgressEvent{Progress: "image_stream_resolve_start", Message: "解析图片结果", Details: map[string]any{"conversation_id": conversationID}})
	uploadedFileIDs := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.FileID != "" {
			uploadedFileIDs = append(uploadedFileIDs, ref.FileID)
		}
	}
	urls, err := c.resolveConversationImageURLs(generationCtx, account, conversationID, fileIDs, sedimentIDs, true, progress, uploadedFileIDs...)
	if err != nil {
		return ImageResult{}, imageAttemptError(ctx, generationCtx, err)
	}
	if len(urls) == 0 {
		return ImageResult{}, fmt.Errorf("upstream completed without generating images")
	}
	out := ImageResult{URLs: urls, ConversationID: conversationID, AccountEmail: account.Email, BackendModel: backendModel}
	if strings.EqualFold(req.ResponseFormat, "b64_json") {
		b64, err := c.downloadBase64(generationCtx, account, urls)
		if err != nil {
			return ImageResult{}, imageAttemptError(ctx, generationCtx, err)
		}
		out.B64JSON = b64
		out.URLs = nil
	}
	return out, nil
}

func imageAttemptError(parent, generationCtx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) && (parent == nil || parent.Err() == nil) {
		return imageGenerationError(generationCtx, err)
	}
	return err
}

func imagePromptForWeb(prompt string, edit bool, size, quality string) string {
	return strings.TrimSpace(prompt)
}

// imageAttemptContext contains every network phase for one account. Setup is
// separately capped so a hung bootstrap or upload cannot consume the full
// generation window, while the outer task context limits account switching.
func (c *Client) imageAttemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, c.imagePreparationBudget()+c.imageGenerationBudget())
}

func (c *Client) imagePreparationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, c.imagePreparationBudget())
}

func (c *Client) imagePreparationBudget() time.Duration {
	timeout := c.imagePreparationTimeout
	if timeout <= 0 {
		return defaultImagePreparationTimeout
	}
	return timeout
}

func (c *Client) imageGenerationBudget() time.Duration {
	timeout := c.pollTimeout
	if timeout <= 0 {
		return 180 * time.Second
	}
	return timeout
}

func (c *Client) imagePreparationError(parent context.Context, err error) error {
	// VM capacity is process-wide congestion, not an account-specific setup
	// timeout. Its wrapped deadline comes from waiting for a VM slot, so keep
	// the sentinel intact for the dispatcher to handle without cooling an
	// otherwise healthy account.
	if errors.Is(err, ErrTurnstileVMCapacity) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) && (parent == nil || parent.Err() == nil) {
		return fmt.Errorf("%w: ChatGPT 生图准备超时（已等待 %.0f 秒）", ErrImagePreparationTimeout, c.imagePreparationBudget().Seconds())
	}
	return err
}

func (c *Client) imageSlug(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || model == "gpt-image-2" {
		return "auto"
	}
	if model == "codex-gpt-image-2" || strings.HasSuffix(model, "-codex-gpt-image-2") {
		return model
	}
	return "auto"
}

func (c *Client) bootstrap(ctx context.Context, account accounts.Account) error {
	_, _, err := c.bootstrapWithResources(ctx, account)
	return err
}

func (c *Client) bootstrapWithResources(ctx context.Context, account accounts.Account) ([]string, string, error) {
	key := bootstrapResourcesKey(account)
	now := c.bootstrapNow()
	c.bootstrapMu.Lock()
	if cached, ok := c.bootstrapResources[key]; ok {
		if now.Before(cached.expiresAt) {
			scripts, build := cloneBootstrapScripts(cached.scripts), cached.build
			c.bootstrapMu.Unlock()
			return scripts, build, nil
		}
		delete(c.bootstrapResources, key)
	}
	if flight := c.bootstrapFlights[key]; flight != nil {
		c.bootstrapMu.Unlock()
		select {
		case <-flight.done:
			return cloneBootstrapScripts(flight.scripts), flight.build, flight.err
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	if c.bootstrapFlights == nil {
		c.bootstrapFlights = map[bootstrapResourcesCacheKey]*bootstrapResourcesFlight{}
	}
	flight := &bootstrapResourcesFlight{done: make(chan struct{})}
	c.bootstrapFlights[key] = flight
	c.bootstrapMu.Unlock()

	scripts, build, err := c.bootstrapWithResourcesUncached(ctx, account)
	c.bootstrapMu.Lock()
	delete(c.bootstrapFlights, key)
	flight.scripts = cloneBootstrapScripts(scripts)
	flight.build = build
	flight.err = err
	if err == nil {
		if c.bootstrapResources == nil {
			c.bootstrapResources = map[bootstrapResourcesCacheKey]bootstrapResourcesCacheEntry{}
		}
		c.bootstrapResources[key] = bootstrapResourcesCacheEntry{scripts: cloneBootstrapScripts(scripts), build: build, expiresAt: c.bootstrapNow().Add(bootstrapResourcesTTL)}
	}
	close(flight.done)
	c.bootstrapMu.Unlock()
	return scripts, build, err
}

func (c *Client) bootstrapWithResourcesUncached(ctx context.Context, account accounts.Account) ([]string, string, error) {
	scripts, build, err := c.bootstrapWithResourcesOnce(ctx, account)
	if err == nil || !isBootstrapCloudflareError(err) || c.bootstrapClearanceRefresh == nil {
		return scripts, build, err
	}
	refreshed, refreshErr := c.bootstrapClearanceRefresh(ctx)
	if refreshErr != nil || refreshed == nil {
		if refreshErr != nil {
			log.Printf("ChatGPT bootstrap HTTP 403; FlareSolverr clearance refresh failed: %v", refreshErr)
		}
		return scripts, build, err
	}
	log.Printf("ChatGPT bootstrap HTTP 403; refreshed FlareSolverr clearance and retrying once")
	scripts, build, err = refreshed.bootstrapWithResourcesOnce(ctx, account)
	if err == nil && refreshed != c {
		refreshed.cacheBootstrapResources(account, scripts, build)
	}
	return scripts, build, err
}

func bootstrapResourcesKey(account accounts.Account) bootstrapResourcesCacheKey {
	return bootstrapResourcesCacheKey{accessToken: strings.TrimSpace(account.AccessToken), proxy: strings.TrimSpace(account.Proxy)}
}

func cloneBootstrapScripts(scripts []string) []string {
	return append([]string(nil), scripts...)
}

func (c *Client) bootstrapNow() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Client) cacheBootstrapResources(account accounts.Account, scripts []string, build string) {
	if c == nil {
		return
	}
	c.bootstrapMu.Lock()
	defer c.bootstrapMu.Unlock()
	if c.bootstrapResources == nil {
		c.bootstrapResources = map[bootstrapResourcesCacheKey]bootstrapResourcesCacheEntry{}
	}
	c.bootstrapResources[bootstrapResourcesKey(account)] = bootstrapResourcesCacheEntry{scripts: cloneBootstrapScripts(scripts), build: build, expiresAt: c.bootstrapNow().Add(bootstrapResourcesTTL)}
}

func (c *Client) bootstrapWithResourcesOnce(ctx context.Context, account accounts.Account) ([]string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return nil, "", err
	}
	for k, v := range c.baseHeaders(account) {
		request.Header.Set(k, v)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	resp, err := c.clientFor(account, false).Do(request)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if err := ensureOK(resp, "bootstrap"); err != nil {
		return nil, "", err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	scripts, build := parsePOWResources(string(data))
	return scripts, build, nil
}

func isBootstrapCloudflareError(err error) bool {
	var upstream *UpstreamError
	return errors.As(err, &upstream) && upstream.StatusCode == http.StatusForbidden
}

func (c *Client) chatRequirements(ctx context.Context, account accounts.Account, scripts []string, dataBuild string) (chatRequirements, error) {
	base := "/backend-api/sentinel/chat-requirements"
	if strings.TrimSpace(account.AccessToken) == "" {
		base = "/backend-anon/sentinel/chat-requirements"
	}
	pToken := buildLegacyRequirementsToken(c.userAgent(account), scripts, dataBuild)
	var prepare map[string]any
	if err := c.doJSON(ctx, account, http.MethodPost, base+"/prepare", base+"/prepare", map[string]any{"p": pToken}, map[string]string{"Content-Type": "application/json"}, &prepare); err != nil {
		if IsTokenInvalidError(err) {
			return chatRequirements{}, fmt.Errorf("token invalidated (%s): %w", base+"/prepare", err)
		}
		return chatRequirements{}, err
	}
	if requiredBool(prepare, "arkose") {
		return chatRequirements{}, fmt.Errorf("chat requirements requires arkose token")
	}
	proofToken := ""
	if po, _ := prepare["proofofwork"].(map[string]any); truthy(po["required"]) {
		token, err := buildProofToken(ctx, str(po["seed"]), str(po["difficulty"]), c.userAgent(account), scripts, dataBuild)
		if err != nil {
			return chatRequirements{}, err
		}
		proofToken = token
	}
	turnstileToken := ""
	if requiredBool(prepare, "turnstile") {
		turnstile, _ := prepare["turnstile"].(map[string]any)
		var err error
		turnstileToken, err = c.resolveTurnstileToken(ctx, account, str(turnstile["dx"]), pToken)
		if err != nil {
			log.Printf("chat requirements Turnstile proof failed: %v", err)
			return chatRequirements{}, fmt.Errorf("chat requirements requires turnstile token: %w", err)
		}
	}
	var finalize map[string]any
	prepareToken := str(prepare["prepare_token"])
	body := map[string]any{"prepare_token": prepareToken, "proof_token": proofToken, "turnstile_token": turnstileToken}
	if err := c.doJSON(ctx, account, http.MethodPost, base+"/finalize", base+"/finalize", body, map[string]string{"Content-Type": "application/json"}, &finalize); err != nil {
		return chatRequirements{}, err
	}
	token := str(finalize["token"])
	if token == "" {
		return chatRequirements{}, fmt.Errorf("missing chat requirements token: %#v", finalize)
	}
	return chatRequirements{Token: token, PrepareToken: prepareToken, ProofToken: proofToken, TurnstileToken: turnstileToken, SOToken: str(finalize["so_token"])}, nil
}

func requiredBool(payload map[string]any, key string) bool {
	child, _ := payload[key].(map[string]any)
	return truthy(child["required"])
}
func truthy(v any) bool { b, _ := v.(bool); return b }
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func (c *Client) prepareImageConversation(ctx context.Context, account accounts.Account, prompt, model string, requirements chatRequirements, refs []uploadMeta) (string, string, string, error) {
	path := "/backend-api/f/conversation/prepare"
	turnTraceID := c.newID()
	parentMessageID := c.newID()
	payload := map[string]any{
		"action":                 "next",
		"fork_from_shared_post":  false,
		"parent_message_id":      parentMessageID,
		"model":                  model,
		"client_prepare_state":   "none",
		"timezone_offset_min":    -480,
		"timezone":               "Asia/Shanghai",
		"conversation_mode":      map[string]any{"kind": "primary_assistant"},
		"system_hints":           []string{},
		"supports_buffering":     true,
		"supported_encodings":    []string{"v1"},
		"client_contextual_info": map[string]any{"app_name": "chatgpt.com"},
		"thinking_effort":        "standard",
	}
	if mimeTypes := imageAttachmentMIMETypes(refs); len(mimeTypes) > 0 {
		payload["attachment_mime_types"] = mimeTypes
	}
	var out struct {
		ConduitToken string `json:"conduit_token"`
	}
	if err := c.doJSON(ctx, account, http.MethodPost, path, path, payload, c.imageHeaders(requirements, "no-token", "*/*", turnTraceID), &out); err != nil {
		return "", "", "", fmt.Errorf("prepare conversation(none): %w", err)
	}
	conduit := strings.TrimSpace(out.ConduitToken)
	if conduit == "" {
		return "", "", "", fmt.Errorf("prepare conversation(none): missing conduit_token")
	}
	return conduit, turnTraceID, parentMessageID, nil
}

func (c *Client) downloadBase64(ctx context.Context, account accounts.Account, urls []string) ([]string, error) {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		data, err := c.downloadImageFor(ctx, account, u)
		if err != nil {
			return nil, err
		}
		out = append(out, base64.StdEncoding.EncodeToString(data))
	}
	return out, nil
}

func (c *Client) DownloadImage(ctx context.Context, imageURL string) ([]byte, error) {
	return c.DownloadImageFor(ctx, accounts.Account{}, imageURL)
}

func (c *Client) DownloadImageFor(ctx context.Context, account accounts.Account, imageURL string) ([]byte, error) {
	return c.downloadImageFor(ctx, account, imageURL)
}

func (c *Client) downloadImageFor(ctx context.Context, account accounts.Account, imageURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	if c.isUpstreamURL(imageURL) {
		for key, value := range c.baseHeaders(account) {
			req.Header.Set(key, value)
		}
		req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	}
	resp, err := c.clientFor(account, true).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return nil, readErr
		}
		if c.isUpstreamURL(imageURL) {
			path := imageURL
			if target, parseErr := url.Parse(imageURL); parseErr == nil {
				path = target.RequestURI()
			}
			return nil, &UpstreamError{Path: path, StatusCode: resp.StatusCode, Body: string(body)}
		}
		return nil, fmt.Errorf("download image status=%d", resp.StatusCode)
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if readErr != nil {
		return nil, readErr
	}
	return data, nil
}

func (c *Client) isUpstreamURL(raw string) bool {
	target, err := url.Parse(raw)
	if err != nil {
		return false
	}
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(target.Scheme, base.Scheme) && strings.EqualFold(target.Host, base.Host)
}

func ensureOK(resp *http.Response, path string) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	retryAfter, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
	return &UpstreamError{Path: path, StatusCode: resp.StatusCode, Body: string(data), RetryAfter: retryAfter}
}

func (c *Client) doJSON(ctx context.Context, account accounts.Account, method, path, route string, payload any, extra map[string]string, out any) error {
	var body io.Reader
	if payload != nil {
		data, _ := json.Marshal(payload)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	for k, v := range c.headers(account, path, route, extra) {
		req.Header.Set(k, v)
	}
	resp, err := c.clientFor(account, false).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := ensureOK(resp, path); err != nil {
		return err
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) clientFor(account accounts.Account, resource bool) *http.Client {
	if c.transport == "tls_client" && !c.customHTTP && strings.TrimSpace(account.AccessToken) != "" {
		return c.tlsClientFor(account, resource)
	}
	if strings.TrimSpace(account.Proxy) == "" {
		if resource {
			return c.resourceClient
		}
		return c.httpClient
	}
	runtime := c.proxyRuntime
	runtime.Enabled = true
	runtime.EgressMode = "single_proxy"
	runtime.ProxyURL = account.Proxy
	runtime.ResourceProxyURL = account.Proxy
	var (
		client *http.Client
		err    error
	)
	if resource {
		client, err = proxy.NewResourceHTTPClientForRuntime(runtime, c.httpClient.Timeout)
	} else {
		client, err = proxy.NewHTTPClientForRuntime(runtime, c.httpClient.Timeout)
	}
	if err == nil && client != nil {
		return client
	}
	if resource {
		return c.resourceClient
	}
	return c.httpClient
}

func (c *Client) streamClientFor(account accounts.Account) *http.Client {
	if c.transport == "tls_client" && !c.customHTTP && strings.TrimSpace(account.AccessToken) != "" {
		return c.tlsStreamClientFor(account)
	}
	client := c.clientFor(account, false)
	if client == nil {
		return nil
	}
	clone := *client
	clone.Timeout = 0
	return &clone
}

func (c *Client) tlsClientFor(account accounts.Account, resource bool) *http.Client {
	key := strings.TrimSpace(account.AccessToken) + "\n" + strings.TrimSpace(account.Proxy)
	c.tlsMu.Lock()
	defer c.tlsMu.Unlock()
	cache := c.tlsClients
	if resource {
		cache = c.tlsResources
	}
	if cached := cache[key]; cached != nil {
		return cached
	}
	runtime := c.proxyRuntime
	if proxyURL := strings.TrimSpace(account.Proxy); proxyURL != "" {
		runtime.Enabled = true
		runtime.EgressMode = "single_proxy"
		runtime.ProxyURL = proxyURL
		runtime.ResourceProxyURL = proxyURL
	}
	client, err := browsertransport.NewHTTPClient(runtime, c.timeout, resource)
	if err != nil || client == nil {
		if resource {
			return c.resourceClient
		}
		return c.httpClient
	}
	cache[key] = client
	return client
}

func (c *Client) tlsStreamClientFor(account accounts.Account) *http.Client {
	key := strings.TrimSpace(account.AccessToken) + "\n" + strings.TrimSpace(account.Proxy)
	c.tlsMu.Lock()
	if cached := c.tlsStreamClients[key]; cached != nil {
		c.tlsMu.Unlock()
		return cached
	}
	c.tlsMu.Unlock()

	// Reuse the normal client's jar so bootstrap cookies remain available to the
	// SSE request, while using a separate client without a total timeout.
	normal := c.tlsClientFor(account, false)
	runtime := c.proxyRuntime
	if proxyURL := strings.TrimSpace(account.Proxy); proxyURL != "" {
		runtime.Enabled = true
		runtime.EgressMode = "single_proxy"
		runtime.ProxyURL = proxyURL
		runtime.ResourceProxyURL = proxyURL
	}
	stream, err := browsertransport.NewStreamingHTTPClient(runtime, false, browsertransport.CookieJarForHTTPClient(normal))
	if err != nil || stream == nil {
		clone := *normal
		clone.Timeout = 0
		return &clone
	}

	c.tlsMu.Lock()
	defer c.tlsMu.Unlock()
	if cached := c.tlsStreamClients[key]; cached != nil {
		return cached
	}
	c.tlsStreamClients[key] = stream
	return stream
}
