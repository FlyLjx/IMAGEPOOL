package openaiweb

import (
	"context"
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
	Prompt         string              `json:"prompt"`
	Model          string              `json:"model"`
	N              int                 `json:"n"`
	Size           string              `json:"size"`
	Quality        string              `json:"quality"`
	ResponseFormat string              `json:"response_format"`
	OutputFormat   string              `json:"output_format"`
	Stream         bool                `json:"stream"`
	References     []ImageInput        `json:"-"`
	OutputBaseURL  string              `json:"-"`
	Progress       func(ProgressEvent) `json:"-"`
}

type ProgressEvent struct {
	Progress string         `json:"progress"`
	Message  string         `json:"message,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

type AttemptLog struct {
	Attempt        int    `json:"attempt"`
	AccountEmail   string `json:"account_email,omitempty"`
	BackendModel   string `json:"backend_model,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	RemovedAccount bool   `json:"removed_account,omitempty"`
}

type ImageResult struct {
	URLs           []string     `json:"urls,omitempty"`
	B64JSON        []string     `json:"b64_json,omitempty"`
	ConversationID string       `json:"conversation_id,omitempty"`
	AccountEmail   string       `json:"account_email,omitempty"`
	BackendModel   string       `json:"backend_model,omitempty"`
	Attempts       []AttemptLog `json:"attempts,omitempty"`
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
	ErrMissingConduitToken       = errors.New("missing conduit_token")
)

const (
	// PublicCredentialInvalidMessage is intentionally independent of the
	// upstream response. Upstream OAuth bodies can contain endpoint and token
	// details which must never reach API consumers or persisted task history.
	PublicCredentialInvalidMessage = "账号凭证已失效，已自动删除并切换账号重试"
	PublicImagePollTimeoutMessage  = "任务占用额度失败，请再次提交。"
	PublicUpstreamFailureMessage   = "上游服务请求失败，请稍后重试"
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
	return PublicErrorText(err.Error())
}

// PublicErrorText redacts raw upstream diagnostics that may already have been
// flattened into a string (for example a persisted legacy task record).
func PublicErrorText(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if IsAuthenticationError(errors.New(message)) {
		return PublicCredentialInvalidMessage
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "image poll timeout") ||
		strings.Contains(message, "ChatGPT 生图任务已等待") ||
		strings.Contains(message, "ChatGPT 生图超时") {
		return PublicImagePollTimeoutMessage
	}
	if strings.Contains(lower, "upstream ") ||
		strings.Contains(lower, "/backend-api/") ||
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

// PublicAttemptLogs copies logs for API output and removes raw upstream
// diagnostics from each attempt without changing the internal source slice.
func PublicAttemptLogs(attempts []AttemptLog) []AttemptLog {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]AttemptLog, len(attempts))
	copy(out, attempts)
	for i := range out {
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
		out[key] = publicDetailValue(value)
	}
	return out
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
		strings.Contains(text, "image quota exhausted")
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
	if errors.Is(err, ErrTurnstileVMCapacity) {
		return true
	}
	text := strings.ToLower(err.Error())
	if IsAuthenticationError(err) || IsNoFreeImageQuotaError(err) || errors.Is(err, ErrPollTimeout) || errors.Is(err, ErrImagePreparationTimeout) || errors.Is(err, ErrImageGenerationTerminated) || errors.Is(err, ErrMissingConduitToken) {
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
