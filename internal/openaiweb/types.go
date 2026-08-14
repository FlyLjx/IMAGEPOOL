package openaiweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"imagepool/internal/accounts"
)

type ImageInput struct {
	Data     []byte
	FileName string
	MIMEType string
	Width    int
	Height   int
}

type ImageRequest struct {
	Prompt         string       `json:"prompt"`
	Model          string       `json:"model"`
	N              int          `json:"n"`
	Size           string       `json:"size"`
	Quality        string       `json:"quality"`
	ResponseFormat string       `json:"response_format"`
	OutputFormat   string       `json:"output_format"`
	Stream         bool         `json:"stream"`
	Async          bool         `json:"async"`
	CallbackURL    string       `json:"callback_url,omitempty"`
	References     []ImageInput `json:"-"`
	OutputBaseURL  string       `json:"-"`
	TaskID         string       `json:"-"`
	OwnerID        string       `json:"-"`
	PublicModel    string       `json:"-"`
	// DispatchState is used by the async task manager to release its local
	// worker slot while this request waits for a global or account lease.
	// It is intentionally internal and never crosses the HTTP boundary.
	DispatchState   func(string)          `json:"-"`
	Progress        func(ProgressEvent)   `json:"-"`
	AccountProgress func(AccountIdentity) `json:"-"`
}

// AccountIdentity is the non-secret account identity used by the internal
// task observer. Tokens are intentionally excluded.
type AccountIdentity struct {
	ID             string
	Email          string
	AvailableQuota *int
}

type ProgressEvent struct {
	Progress string         `json:"progress"`
	Message  string         `json:"message,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

type AttemptLog struct {
	Attempt             int                      `json:"attempt,omitempty"`
	AccountID           string                   `json:"account_id,omitempty"`
	AccountEmail        string                   `json:"account_email,omitempty"`
	LeaseID             string                   `json:"lease_id,omitempty"`
	BackendModel        string                   `json:"backend_model,omitempty"`
	ConversationID      string                   `json:"conversation_id,omitempty"`
	Status              string                   `json:"status"`
	Phase               string                   `json:"phase,omitempty"`
	PollCount           int                      `json:"poll_count,omitempty"`
	LastHTTPStatus      int                      `json:"last_http_status,omitempty"`
	EmptyResultSecs     int                      `json:"empty_result_secs,omitempty"`
	ToolSeen            bool                     `json:"tool_seen"`
	ImageReferenceSeen  bool                     `json:"image_reference_seen"`
	AssistantTextSeen   bool                     `json:"assistant_text_seen"`
	LastRole            string                   `json:"last_role,omitempty"`
	ResultSignature     string                   `json:"result_signature,omitempty"`
	SwitchReason        string                   `json:"switch_reason,omitempty"`
	CooldownUntil       string                   `json:"cooldown_until,omitempty"`
	ActiveSlots         int                      `json:"active_slots,omitempty"`
	EffectiveLimit      int                      `json:"effective_limit,omitempty"`
	ConfiguredCeiling   int                      `json:"configured_ceiling,omitempty"`
	DurationMS          int64                    `json:"duration_ms,omitempty"`
	Error               string                   `json:"error,omitempty"`
	RemovedAccount      bool                     `json:"removed_account,omitempty"`
	ResponseDiagnostics ImageResponseDiagnostics `json:"-"`
}

type ImageAttemptDiagnostics struct {
	Phase              string
	PollCount          int
	LastHTTPStatus     int
	EmptyResultSecs    int
	ToolSeen           bool
	ImageReferenceSeen bool
	AssistantTextSeen  bool
	LastRole           string
	ResultSignature    string
	Response           ImageResponseDiagnostics
}

type ImageResult struct {
	URLs           []string                `json:"urls,omitempty"`
	B64JSON        []string                `json:"b64_json,omitempty"`
	ConversationID string                  `json:"conversation_id,omitempty"`
	AccountEmail   string                  `json:"account_email,omitempty"`
	BackendModel   string                  `json:"backend_model,omitempty"`
	Attempts       []AttemptLog            `json:"attempts,omitempty"`
	Diagnostics    ImageAttemptDiagnostics `json:"-"`
}

type AccountInfo struct {
	Email             string           `json:"email"`
	Type              string           `json:"type"`
	Quota             int              `json:"quota"`
	ImageQuotaUnknown bool             `json:"image_quota_unknown"`
	LimitsProgress    []map[string]any `json:"limits_progress,omitempty"`
	RestoreAt         string           `json:"restore_at,omitempty"`
	DefaultModelSlug  string           `json:"default_model_slug,omitempty"`
}

type Backend interface {
	GenerateImage(ctx context.Context, account accounts.Account, req ImageRequest) (ImageResult, error)
	ListModels(ctx context.Context, token string) ([]string, error)
	Search(ctx context.Context, account accounts.Account, req SearchRequest) (SearchResult, error)
}

var (
	ErrContentPolicy             = errors.New("content policy violation")
	ErrPollTimeout               = errors.New("image poll timeout")
	ErrImagePreparationTimeout   = errors.New("image preparation timeout")
	ErrImageGenerationTerminated = errors.New("image generation terminated")
	ErrImageGenerationStalled    = errors.New("image generation stalled")
	ErrImageReferenceRequired    = errors.New("image reference required")
	ErrImageAssistantText        = errors.New("image generation returned text without an image")
	ErrMissingConduitToken       = errors.New("missing conduit_token")
)

type ImageGenerationStalledError struct {
	ConversationID string
	ElapsedSecs    int
	Diagnostics    ImageAttemptDiagnostics
}

func (e *ImageGenerationStalledError) Error() string {
	elapsed := e.ElapsedSecs
	if elapsed <= 0 {
		elapsed = e.Diagnostics.EmptyResultSecs
	}
	if elapsed <= 0 {
		elapsed = 1
	}
	conversationID := strings.TrimSpace(e.ConversationID)
	if conversationID == "" {
		return fmt.Sprintf("%s: 图片会话连续 %d 秒没有图片结果", ErrImageGenerationStalled, elapsed)
	}
	return fmt.Sprintf("%s: 图片会话连续 %d 秒没有图片结果; conversation_id=%s", ErrImageGenerationStalled, elapsed, conversationID)
}

func (e *ImageGenerationStalledError) Unwrap() error {
	return ErrImageGenerationStalled
}

func NewImageGenerationStalledError(conversationID string, elapsedSecs int, diagnostics ImageAttemptDiagnostics) error {
	if elapsedSecs <= 0 {
		elapsedSecs = diagnostics.EmptyResultSecs
	}
	if elapsedSecs <= 0 {
		elapsedSecs = 1
	}
	return &ImageGenerationStalledError{ConversationID: strings.TrimSpace(conversationID), ElapsedSecs: elapsedSecs, Diagnostics: diagnostics}
}

func IsImageGenerationStalled(err error) bool {
	var stalled *ImageGenerationStalledError
	return errors.As(err, &stalled)
}

// ImageReferenceRequiredError marks an upstream assistant response that
// explicitly asks the caller to upload a reference image or thumbnail. This
// is a request/input problem, not an account failure: the current lease must
// end and the public request should receive a 400 without switching accounts.
type ImageReferenceRequiredError struct {
	ConversationID string
	Diagnostics    ImageAttemptDiagnostics
}

func (e *ImageReferenceRequiredError) Error() string {
	if strings.TrimSpace(e.ConversationID) == "" {
		return fmt.Sprintf("%s: 上游要求上传参考图或缩略图", ErrImageReferenceRequired)
	}
	return fmt.Sprintf("%s: 上游要求上传参考图或缩略图; conversation_id=%s", ErrImageReferenceRequired, strings.TrimSpace(e.ConversationID))
}

func (e *ImageReferenceRequiredError) Unwrap() error {
	return ErrImageReferenceRequired
}

func NewImageReferenceRequiredError(conversationID string, diagnostics ImageAttemptDiagnostics) error {
	return &ImageReferenceRequiredError{
		ConversationID: strings.TrimSpace(conversationID),
		Diagnostics:    diagnostics,
	}
}

func IsImageReferenceRequired(err error) bool {
	return errors.Is(err, ErrImageReferenceRequired)
}

// ImageAssistantTextError marks an accepted image conversation that did not
// return a generated image ID. The assistant text is preserved when present
// so callers can return the actual OpenAI message without retrying an account.
type ImageAssistantTextError struct {
	ConversationID string
	Message        string
	Diagnostics    ImageAttemptDiagnostics
	ContentPolicy  bool
}

func (e *ImageAssistantTextError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "未识别到 OpenAI 返回的图片 ID。"
	}
	if strings.TrimSpace(e.ConversationID) == "" {
		return fmt.Sprintf("%s: %s", ErrImageAssistantText, message)
	}
	return fmt.Sprintf("%s: %s; conversation_id=%s", ErrImageAssistantText, message, strings.TrimSpace(e.ConversationID))
}

func (e *ImageAssistantTextError) Unwrap() error {
	if e.ContentPolicy {
		return errors.Join(ErrImageAssistantText, ErrContentPolicy)
	}
	return ErrImageAssistantText
}

func NewImageAssistantTextError(conversationID, message string, diagnostics ImageAttemptDiagnostics, contentPolicy bool) error {
	return &ImageAssistantTextError{
		ConversationID: strings.TrimSpace(conversationID),
		Message:        strings.TrimSpace(message),
		Diagnostics:    diagnostics,
		ContentPolicy:  contentPolicy,
	}
}

func ImageAssistantText(err error) (string, bool) {
	var textErr *ImageAssistantTextError
	if !errors.As(err, &textErr) {
		return "", false
	}
	return strings.TrimSpace(textErr.Message), true
}

// IsSkippedMainlineImageError reports an internal response where the image
// generation mainline was skipped before any image ID was produced. Retrying
// on a different account is appropriate for this transient execution state.
func IsSkippedMainlineImageError(err error) bool {
	text, ok := ImageAssistantText(err)
	if !ok {
		return false
	}
	var value struct {
		SkippedMainline bool `json:"skipped_mainline"`
	}
	return json.Unmarshal([]byte(strings.TrimSpace(text)), &value) == nil && value.SkippedMainline
}

// IsReferencedImageIDsRetryError identifies an upstream response that echoes
// uploaded reference file IDs without returning generated image IDs. The files
// were accepted by the current account, yet the image-generation turn was not
// started, so retrying the original request on another account is appropriate.
func IsReferencedImageIDsRetryError(err error) bool {
	_, referenced := imageRequestEchoDetails(err)
	return referenced
}

// IsImageRequestEchoRetryError identifies an upstream response that only
// echoes image-generation request fields instead of starting an image tool.
// This is distinct from an assistant's natural-language answer, so retrying
// the original request with another account is appropriate.
func IsImageRequestEchoRetryError(err error) bool {
	echoed, _ := imageRequestEchoDetails(err)
	return echoed
}

func imageRequestEchoDetails(err error) (echoed, referenced bool) {
	text, ok := ImageAssistantText(err)
	if !ok {
		return false, false
	}
	text = strings.TrimSpace(text)
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &raw) != nil {
		return truncatedImageRequestEchoDetails(text)
	}
	var value struct {
		ReferencedImageIDs []string `json:"referenced_image_ids"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &value) == nil && len(value.ReferencedImageIDs) > 0 {
		return true, true
	}
	prompt, ok := raw["prompt"]
	if !ok {
		return false, false
	}
	var promptText string
	promptIsText := json.Unmarshal(prompt, &promptText) == nil && strings.TrimSpace(promptText) != ""
	imageFieldCount := 0
	for _, field := range []string{"size", "n", "model", "quality", "response_format", "output_format", "is_style_transfer", "transparent_background", "referenced_image_ids"} {
		if _, ok := raw[field]; ok {
			imageFieldCount++
		}
	}
	if promptIsText {
		return imageFieldCount > 0, false
	}
	// A null prompt is also emitted by the image tool argument schema when the
	// upstream turn fails to start. Require several companion fields so an
	// unrelated JSON object containing prompt:null is not treated as an echo.
	return strings.TrimSpace(string(prompt)) == "null" && imageFieldCount >= 3, false
}

// truncatedImageRequestEchoDetails recognizes an incomplete image-tool
// argument object observed while the upstream response is still in progress.
// It intentionally requires a JSON-object prefix, prompt, and multiple exact
// image fields so ordinary assistant prose and user prompt text do not match.
func truncatedImageRequestEchoDetails(text string) (echoed, referenced bool) {
	if !strings.HasPrefix(strings.TrimSpace(text), "{") || !strings.Contains(text, `"prompt"`) {
		return false, false
	}
	imageFieldCount := 0
	for _, field := range []string{"size", "n", "model", "quality", "response_format", "output_format", "is_style_transfer", "transparent_background", "referenced_image_ids"} {
		if strings.Contains(text, `"`+field+`"`) {
			imageFieldCount++
		}
	}
	if imageFieldCount < 2 {
		return false, false
	}
	referenced = strings.Contains(text, `"referenced_image_ids"`) && !strings.Contains(text, `"referenced_image_ids":null`)
	return true, referenced
}

// ImageConversationTimeoutError marks the case where ChatGPT has already
// accepted an image conversation but the generated image is still not available
// by the configured poll budget. The same account and conversation must be
// allowed to finish until this timeout; switching accounts cannot retrieve that
// already-submitted conversation.
type ImageConversationTimeoutError struct {
	ConversationID string
	ElapsedSecs    int
}

func (e *ImageConversationTimeoutError) Error() string {
	elapsed := e.ElapsedSecs
	if elapsed <= 0 {
		elapsed = 1
	}
	conversationID := strings.TrimSpace(e.ConversationID)
	if conversationID == "" {
		return fmt.Sprintf("%s: ChatGPT 生图任务已等待 %d 秒", ErrPollTimeout, elapsed)
	}
	return fmt.Sprintf("%s: ChatGPT 生图任务已等待 %d 秒; conversation_id=%s", ErrPollTimeout, elapsed, conversationID)
}

func (e *ImageConversationTimeoutError) Unwrap() error {
	return ErrPollTimeout
}

func NewImageConversationTimeoutError(conversationID string, elapsedSecs int) error {
	if elapsedSecs <= 0 {
		elapsedSecs = 1
	}
	return &ImageConversationTimeoutError{ConversationID: strings.TrimSpace(conversationID), ElapsedSecs: elapsedSecs}
}

func IsImageConversationTimeout(err error) bool {
	var timeout *ImageConversationTimeoutError
	return errors.As(err, &timeout) && strings.TrimSpace(timeout.ConversationID) != ""
}

const (
	// PublicCredentialInvalidMessage is intentionally independent of the
	// upstream response. Upstream OAuth bodies can contain endpoint and token
	// details which must never reach API consumers or persisted task history.
	PublicCredentialInvalidMessage      = "账号认证状态异常，系统正在自动恢复。"
	PublicImagePollTimeoutMessage       = "图片生成超过10分钟仍未完成，请重新提交任务。"
	PublicImageReferenceRequiredMessage = "检测到请求需要缩略图或参考图，请上传后重新提交任务。"
	PublicUpstreamFailureMessage        = "图片服务暂时不可用，请稍后重试。"
)

type UpstreamError struct {
	Path       string
	StatusCode int
	Body       string
	RetryAfter int
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream %s status=%d body=%s", e.Path, e.StatusCode, e.Body)
}

// IsImageReferenceUploadRateLimited distinguishes a file-upload quota from
// a generation-endpoint quota. The former only affects requests that carry
// reference images; plain text-to-image requests can keep using the account.
func IsImageReferenceUploadRateLimited(err error) bool {
	var stage *imageUploadStageError
	if !errors.As(err, &stage) {
		return false
	}
	var upstream *UpstreamError
	return errors.As(stage.Err, &upstream) && upstream.StatusCode == 429
}

// PublicErrorMessage returns an API-safe representation of err. It must only
// be used at output boundaries: callers still receive the original error and
// can therefore classify a revoked credential and remove or retry it.
func PublicErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if IsAuthenticationError(err) {
		return PublicCredentialInvalidMessage
	}
	if errors.Is(err, ErrPollTimeout) {
		return PublicImagePollTimeoutMessage
	}
	if errors.Is(err, ErrImageGenerationStalled) {
		return "图片生成长时间没有返回结果，请重新提交。"
	}
	if IsImageReferenceRequired(err) {
		return PublicImageReferenceRequiredMessage
	}
	var providerErr *UpstreamError
	if errors.As(err, &providerErr) {
		if message := publicStructuredErrorMessage(providerErr); message != "" {
			return message
		}
	}
	return PublicErrorText(err.Error())
}

func publicStructuredErrorMessage(err *UpstreamError) string {
	if err == nil {
		return ""
	}
	body := strings.TrimSpace(err.Body)
	candidates := []string{body}
	if start := strings.Index(body, "{"); start > 0 {
		candidates = append(candidates, body[start:])
	}
	for _, candidate := range candidates {
		var payload struct {
			Error *struct {
				Message string `json:"message"`
				Title   string `json:"title"`
				Hint    string `json:"hint"`
			} `json:"error"`
			Message string `json:"message"`
			Title   string `json:"title"`
			Hint    string `json:"hint"`
		}
		if json.Unmarshal([]byte(candidate), &payload) != nil {
			continue
		}
		message, hint := payload.Message, payload.Hint
		if payload.Error != nil {
			message, hint = payload.Error.Message, payload.Error.Hint
			if strings.TrimSpace(payload.Error.Title) != "" && strings.TrimSpace(message) == "" {
				message = payload.Error.Title
			}
		}
		message = PublicErrorText(message)
		hint = PublicErrorText(hint)
		if message == "" {
			continue
		}
		if hint != "" && !strings.EqualFold(hint, message) {
			message += "；建议：" + hint
		}
		return message
	}
	return ""
}

// PublicErrorText redacts raw upstream diagnostics that may already have been
// flattened into a string (for example a persisted legacy task record).
func PublicErrorText(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if isSkippedMainlineErrorText(message) {
		return "本次请求未触发生图流程，请修改提示词后重新提交。"
	}
	const imageAssistantTextPrefix = "image generation returned text without an image:"
	if strings.HasPrefix(strings.ToLower(message), imageAssistantTextPrefix) {
		text := strings.TrimSpace(message[len(imageAssistantTextPrefix):])
		if marker := strings.Index(strings.ToLower(text), "; conversation_id="); marker >= 0 {
			text = strings.TrimSpace(text[:marker])
		}
		if text == "" {
			return "图片服务返回了文本，但没有生成图片。"
		}
		return text
	}
	if IsAuthenticationError(errors.New(message)) {
		return PublicCredentialInvalidMessage
	}
	if strings.Contains(message, "账号凭证失效") || strings.Contains(message, "切换账号重试") || strings.Contains(message, "删除账号") {
		return PublicCredentialInvalidMessage
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "image poll timeout") ||
		strings.Contains(message, "ChatGPT 生图任务已等待") ||
		strings.Contains(message, "ChatGPT 生图超时") {
		return PublicImagePollTimeoutMessage
	}
	if strings.Contains(lower, "image generation stalled") || strings.Contains(message, "连续") && strings.Contains(message, "没有图片结果") {
		return "图片生成长时间没有返回结果，请重新提交。"
	}
	if strings.Contains(lower, "image reference required") || strings.Contains(message, "上游要求上传缩略图") || strings.Contains(message, "上游要求上传参考图") {
		return PublicImageReferenceRequiredMessage
	}
	if strings.Contains(message, "上游") ||
		strings.Contains(lower, "upstream") ||
		strings.Contains(lower, "provider") ||
		strings.Contains(lower, "oai") ||
		strings.Contains(lower, "chatgpt") ||
		strings.Contains(lower, "/backend-api/") ||
		strings.Contains(lower, "/backend-anon/") ||
		strings.Contains(lower, "/v1/") ||
		strings.Contains(lower, "https://") ||
		strings.Contains(lower, "http://") ||
		strings.Contains(lower, "access_token") ||
		strings.Contains(lower, "refresh_token") ||
		strings.Contains(lower, "id_token") ||
		strings.Contains(lower, "authorization:") ||
		strings.Contains(lower, "bearer ") ||
		strings.Contains(lower, "oauth token") {
		return PublicUpstreamFailureMessage
	}
	return message
}

func isSkippedMainlineErrorText(message string) bool {
	start := strings.Index(message, `{"skipped_mainline"`)
	if start < 0 {
		return false
	}
	var value struct {
		SkippedMainline bool `json:"skipped_mainline"`
	}
	end := strings.Index(message[start:], "}")
	if end < 0 {
		return false
	}
	return json.Unmarshal([]byte(message[start:start+end+1]), &value) == nil && value.SkippedMainline
}

// PublicAttemptLogs copies logs for API output and removes raw upstream
// diagnostics from each attempt without changing the internal source slice.
func PublicAttemptLogs(attempts []AttemptLog) []AttemptLog {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]AttemptLog, len(attempts))
	copy(out, attempts)
	for i := range out {
		out[i].Attempt = 0
		out[i].AccountID = ""
		out[i].AccountEmail = ""
		out[i].RemovedAccount = false
		out[i].Error = PublicErrorText(out[i].Error)
	}
	return out
}

// PublicProgressEvent copies an event before it is stored or emitted to a
// client. Retry diagnostics are sometimes carried in Details rather than the
// human-readable message, so both locations are sanitized.
func PublicProgressEvent(event ProgressEvent) ProgressEvent {
	event.Message = PublicErrorText(event.Message)
	event.Details = PublicDetails(event.Details)
	return event
}

// PublicDetails recursively copies common JSON-compatible detail values and
// redacts embedded error strings. Values outside these forms are left intact.
func PublicDetails(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}
	out := make(map[string]any, len(details))
	for key, value := range details {
		if !publicDetailKeyAllowed(key) {
			continue
		}
		out[key] = publicDetailValue(value)
	}
	return out
}

func publicDetailKeyAllowed(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "account_id", "account_email", "account", "email", "attempt", "attempts", "retry", "max_retries", "removed_account", "used_account_count", "failed_account_count", "image_route_attempt_count":
		return false
	default:
		return true
	}
}

func publicDetailValue(value any) any {
	switch typed := value.(type) {
	case string:
		return PublicErrorText(typed)
	case map[string]any:
		return PublicDetails(typed)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = publicDetailValue(typed[i])
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			out[key] = PublicErrorText(item)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i := range typed {
			out[i] = PublicErrorText(typed[i])
		}
		return out
	default:
		return value
	}
}

func IsTokenInvalidError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "token_invalidated") ||
		strings.Contains(text, "token invalidated") ||
		strings.Contains(text, "token_revoked") ||
		strings.Contains(text, "authentication token has been invalidated") ||
		strings.Contains(text, "invalidated oauth token")
}

// IsAuthenticationError includes explicit OAuth revocation and ordinary
// upstream 401 responses. Callers remove these accounts from the pool.
func IsAuthenticationError(err error) bool {
	if err == nil {
		return false
	}
	if IsTokenInvalidError(err) {
		return true
	}
	var upstream *UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode == 401 {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "status=401") || strings.Contains(text, "http 401") || strings.Contains(text, "http status 401")
}

func IsNoFreeImageQuotaError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no available free image quota") ||
		strings.Contains(text, "no free image quota") ||
		strings.Contains(text, "image quota exhausted") ||
		strings.Contains(text, "free plan limit for image generations requests")
}

// IsInteractiveChallengeError reports an upstream anti-automation challenge
// that must be completed in a browser session. It is not an account failure.
func IsInteractiveChallengeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTurnstileVMCapacity) {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "chat requirements requires turnstile token") ||
		strings.Contains(text, "chat requirements requires arkose token")
}

func IsConversationInaccessibleError(err error) bool {
	if err == nil {
		return false
	}
	var upstream *UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode == 404 {
		return strings.Contains(strings.ToLower(upstream.Body), "conversation_inaccessible")
	}
	return strings.Contains(strings.ToLower(err.Error()), "conversation_inaccessible")
}

func IsRetryableImageError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrContentPolicy) {
		return false
	}
	if IsImageReferenceRequired(err) {
		return false
	}
	if errors.Is(err, ErrImageAssistantText) {
		return IsSkippedMainlineImageError(err) || IsImageRequestEchoRetryError(err)
	}
	if IsImageConversationTimeout(err) {
		return false
	}
	if errors.Is(err, ErrTurnstileVMCapacity) {
		return true
	}
	text := strings.ToLower(err.Error())
	if IsAuthenticationError(err) || IsNoFreeImageQuotaError(err) || errors.Is(err, ErrPollTimeout) || errors.Is(err, ErrImagePreparationTimeout) || errors.Is(err, ErrImageGenerationTerminated) || errors.Is(err, ErrImageGenerationStalled) || errors.Is(err, ErrMissingConduitToken) {
		return true
	}
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case 408, 409, 425, 429:
			return true
		default:
			return upstream.StatusCode >= 500
		}
	}
	return strings.Contains(text, "image generation failed") ||
		strings.Contains(text, "failed to generate image") ||
		strings.Contains(text, "upstream completed without generating images") ||
		strings.Contains(text, "missing conduit_token") ||
		strings.Contains(text, "no image generated") ||
		strings.Contains(text, "result could not be retrieved") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "502") || strings.Contains(text, "503") || strings.Contains(text, "504")
}

type SearchRequest struct {
	Prompt           string
	Model            string
	TimeoutSecs      float64
	PollIntervalSecs float64
}

type SearchSource struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Snippet    string `json:"snippet,omitempty"`
	SourceType string `json:"source_type,omitempty"`
}

type SearchResult struct {
	ConversationID     string         `json:"conversation_id,omitempty"`
	Status             string         `json:"status,omitempty"`
	Answer             string         `json:"answer"`
	Sources            []SearchSource `json:"sources"`
	AssistantMessageID string         `json:"assistant_message_id,omitempty"`
	CreateTime         float64        `json:"create_time,omitempty"`
	AccountEmail       string         `json:"account_email,omitempty"`
	Model              string         `json:"model,omitempty"`
}
