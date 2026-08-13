package errorinfo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"imagepool/internal/accounts"
	"imagepool/internal/auth"
	"imagepool/internal/openaiweb"
)

const StatusClientClosedRequest = 499

type Info struct {
	Title      string `json:"title"`
	Message    string `json:"message"`
	Type       string `json:"type"`
	Code       string `json:"code"`
	Category   string `json:"category"`
	Retryable  bool   `json:"retryable"`
	Action     string `json:"action"`
	Hint       string `json:"hint,omitempty"`
	HTTPStatus int    `json:"-"`
}

const publicServiceCategory = "service"

const imageRequestEchoFallbackMessage = "非常抱歉，生成的图片可能违反了关于暴力内容的防护限制。如果你认为此判断有误，请重试或修改提示语。"

var (
	publicURLPattern      = regexp.MustCompile(`(?i)https?://[^\s"'<>，。；：]+`)
	publicEndpointPattern = regexp.MustCompile(`(?i)(?:/backend-api/|/backend-anon/|/v1/)[^\s"'<>，。；：,}]*`)
	publicCodePattern     = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,79}$`)
)

type upstreamErrorEnvelope struct {
	Error   *upstreamErrorFields `json:"error"`
	Message string               `json:"message"`
	Title   string               `json:"title"`
	Type    string               `json:"type"`
	Code    string               `json:"code"`
	Hint    string               `json:"hint"`
}

type upstreamErrorFields struct {
	Title     string `json:"title"`
	Message   string `json:"message"`
	Type      string `json:"type"`
	Code      string `json:"code"`
	Category  string `json:"category"`
	Retryable *bool  `json:"retryable"`
	Action    string `json:"action"`
	Hint      string `json:"hint"`
}

func Classify(err error, statusHint int) Info {
	if err == nil {
		return fallback(statusHint)
	}
	text := strings.TrimSpace(err.Error())
	lower := strings.ToLower(text)

	if errors.Is(err, context.Canceled) {
		return info("任务已取消", "请求已取消。", "request_canceled", "canceled", false, "none", "", StatusClientClosedRequest)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(lower, "unexpected eof") {
		return info("请求体不完整", "请求体在传输过程中被截断，参考图可能过大。请压缩图片后重试。", "request_body_incomplete", "request", false, "check_request", "请检查图片大小后重试", http.StatusBadRequest)
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return info("请求体过大", "参考图文件过大，请压缩图片或改用图片链接后重试。", "request_body_too_large", "request", false, "check_request", "请检查图片大小后重试", http.StatusRequestEntityTooLarge)
	}
	if openaiweb.IsImageReferenceRequired(err) || strings.Contains(lower, "image reference required") || strings.Contains(text, "上游要求上传缩略图") || strings.Contains(text, "上游要求上传参考图") {
		return info("需要上传参考图", "检测到请求需要缩略图或参考图，请上传后重新提交任务。", "reference_image_required", "request", false, "check_request", "请上传缩略图或参考图文件", http.StatusBadRequest)
	}
	// Quota messages can arrive as assistant text after the image tool starts.
	// Classify them before generic assistant text so callers receive a retryable
	// pool-capacity error when every available account has been exhausted.
	if openaiweb.IsNoFreeImageQuotaError(err) {
		return info("号池额度不足", "当前号池生图额度不足，请稍后重试。", "image_quota_exhausted", "capacity", true, "retry_later", "等待号池补充额度后重试", http.StatusTooManyRequests)
	}
	if assistantText, ok := openaiweb.ImageAssistantText(err); ok {
		if openaiweb.IsImageRequestEchoRetryError(err) {
			return info("内容安全限制", imageRequestEchoFallbackMessage, "content_policy_violation", "policy", false, "modify_content", "请根据返回内容修改后重新提交", http.StatusBadRequest)
		}
		if isSkippedMainlineText(assistantText) {
			return info("图片未生成", "本次请求未触发生图流程，请修改提示词后重新提交。", "image_generation_not_started", "request", false, "check_request", "请修改提示词后重新提交", http.StatusBadRequest)
		}
		// image_response_text is the upstream message shown by the assistant
		// after an accepted image turn. Keep it verbatim for management-side
		// diagnosis instead of turning it into a generic request error.
		assistantText = strings.TrimSpace(assistantText)
		if assistantText == "" {
			assistantText = "未识别到 OpenAI 返回的图片 ID。"
		}
		if errors.Is(err, openaiweb.ErrContentPolicy) {
			return info("内容安全限制", assistantText, "content_policy_violation", "policy", false, "modify_content", "请根据返回内容修改后重新提交", http.StatusBadRequest)
		}
		return info("图片未生成", assistantText, "image_response_text", "request", false, "check_request", "", http.StatusBadRequest)
	}
	if errors.Is(err, openaiweb.ErrContentPolicy) || isContentPolicyText(text) {
		return info("内容安全限制", "提交内容触发了安全限制，请调整提示词或参考图后重试。", "content_policy_violation", "policy", false, "modify_content", "调整提示词或参考图后重新提交", http.StatusBadRequest)
	}
	if errors.Is(err, openaiweb.ErrImageGenerationTerminated) || strings.Contains(lower, "image_generation_failed") || strings.Contains(lower, "image generation failed") {
		return info("图片生成未完成", "本次图片生成未完成，系统已自动重试，请重新提交。", "image_generation_failed", publicServiceCategory, true, "retry_request", "请重新提交任务", http.StatusBadGateway)
	}
	if errors.Is(err, openaiweb.ErrImageGenerationStalled) {
		return info("图片生成长时间无结果", "图片生成长时间没有返回结果，系统已重新选择账号继续处理。", "image_generation_stalled", publicServiceCategory, true, "retry_request", "请重新提交任务", http.StatusTooManyRequests)
	}
	if errors.Is(err, openaiweb.ErrPollTimeout) || strings.Contains(lower, "image poll timeout") || strings.Contains(text, "生图任务已等待") || strings.Contains(text, "OAI侧出图超出") || strings.Contains(text, "任务占用额度失败") {
		return info("图片生成超时", "图片生成在 10 分钟内未完成，本次任务已结束，请重新提交。", "image_generation_timeout", publicServiceCategory, true, "retry_request", "请重新提交任务", http.StatusTooManyRequests)
	}
	if errors.Is(err, openaiweb.ErrImagePreparationTimeout) {
		if strings.Contains(text, "参考图") || strings.Contains(lower, "upload") {
			return info("参考图上传超时", "参考图上传暂时超时，请重新提交。", "image_upload_timeout", publicServiceCategory, true, "retry_request", "请重新提交任务", http.StatusGatewayTimeout)
		}
		return info("生图会话准备超时", "图片生成服务准备会话超时，请重新提交。", "image_preparation_timeout", publicServiceCategory, true, "retry_request", "请重新提交任务", http.StatusGatewayTimeout)
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "context deadline exceeded") {
		return info("服务响应超时", "图片生成服务在规定时间内未响应，请稍后重试。", "service_timeout", publicServiceCategory, true, "retry_later", "稍后重新提交任务", http.StatusGatewayTimeout)
	}
	if errors.Is(err, openaiweb.ErrMissingConduitToken) || strings.Contains(lower, "missing conduit_token") || strings.Contains(lower, "conversation_id not found") {
		return info("生图会话建立失败", "图片生成服务暂时无法建立会话，系统已自动重试，请重新提交。", "image_session_failed", publicServiceCategory, true, "retry_request", "请重新提交任务", http.StatusBadGateway)
	}
	if openaiweb.IsInteractiveChallengeError(err) {
		return info("需要完成人机验证", "当前图片服务要求完成人机验证，暂时无法继续处理。", "interactive_challenge_required", publicServiceCategory, true, "retry_later", "请稍后重试", http.StatusPreconditionRequired)
	}
	if errors.Is(err, accounts.ErrNoAvailableAccount) {
		return info("暂无可用处理资源", "当前没有可调度的生图资源，请稍后重试。", "account_pool_unavailable", "capacity", true, "retry_later", "请稍后重新提交", http.StatusTooManyRequests)
	}
	if openaiweb.IsAuthenticationError(err) || strings.Contains(text, openaiweb.PublicCredentialInvalidMessage) || strings.Contains(text, "账号凭证已失效") {
		return info("生图服务暂不可用", "当前生图服务暂时不可用，请稍后重新提交。", "account_pool_unavailable", "capacity", true, "retry_later", "请稍后重新提交", http.StatusTooManyRequests)
	}

	var quota *auth.QuotaError
	if errors.As(err, &quota) {
		return classifyQuota(quota)
	}
	if strings.Contains(lower, "任务队列已满") || strings.Contains(lower, "task queue") {
		return info("任务队列已满", "当前请求较多，任务队列已满，请稍后重新提交。", "task_queue_full", "capacity", true, "retry_later", "请稍后重新提交", http.StatusTooManyRequests)
	}
	if strings.Contains(text, "服务重启") || strings.Contains(text, "服务停止") {
		return info("服务任务已中断", "服务更新导致任务中断，请重新提交。", "service_restarted", "system", true, "retry_request", "请重新提交任务", http.StatusServiceUnavailable)
	}
	if strings.Contains(lower, "invalid output_format") || strings.Contains(lower, "invalid response_format") {
		return info("图片返回格式不支持", "请求的图片返回格式不受支持，请检查 output_format 和 response_format。", "invalid_output_format", "request", false, "check_request", "请修改请求参数", http.StatusBadRequest)
	}
	if strings.Contains(lower, "prompt is required") || strings.Contains(lower, "messages or prompt is required") {
		return info("缺少提示词", "请求缺少提示词，请补充后重新提交。", "prompt_required", "request", false, "check_request", "请补充 prompt", http.StatusBadRequest)
	}
	if strings.Contains(lower, "empty image") || strings.Contains(lower, "cannot identify image") || strings.Contains(text, "缺少参考图") {
		return info("参考图无效", "参考图缺失或无法识别，请更换图片后重试。", "reference_image_invalid", "request", false, "check_request", "请检查参考图文件", http.StatusBadRequest)
	}
	if strings.Contains(lower, "upstream completed without generating images") || strings.Contains(lower, "no image generated") || strings.Contains(lower, "result could not be retrieved") {
		return info("图片生成结果暂不可用", "任务已结束，但没有返回可用图片，请重新提交。", "image_result_unavailable", publicServiceCategory, true, "retry_request", "请重新提交任务", http.StatusBadGateway)
	}
	if strings.Contains(lower, "provider error") {
		if fields, ok := parseUpstreamError(text); ok {
			status := statusHint
			if status <= 0 {
				status = http.StatusBadGateway
				if strings.EqualFold(strings.TrimSpace(fields.Category), "request") {
					status = http.StatusBadRequest
				}
			}
			return classifyStructuredError(fields, status, text)
		}
		return info("服务请求失败", "服务暂时没有返回详细原因，请稍后重试。", "service_error", publicServiceCategory, true, "retry_later", "请稍后重新提交", http.StatusBadGateway)
	}

	var upstream *openaiweb.UpstreamError
	if errors.As(err, &upstream) {
		if classified, ok := classifyUpstreamError(upstream); ok {
			return classified
		}
		if strings.Contains(strings.ToLower(upstream.Path), "/files") {
			return info("参考图上传失败", "参考图上传暂时不可用，请重新提交。", "image_upload_failed", publicServiceCategory, true, "retry_request", "请重新提交任务", http.StatusBadGateway)
		}
		switch upstream.StatusCode {
		case http.StatusTooManyRequests:
			return info("请求频率受限", "当前请求较多，请稍后重试。", "service_rate_limited", publicServiceCategory, true, "retry_later", "请稍后重新提交", http.StatusTooManyRequests)
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return info("服务响应超时", "图片生成服务响应超时，请稍后重试。", "service_timeout", publicServiceCategory, true, "retry_later", "请稍后重新提交", http.StatusGatewayTimeout)
		case http.StatusServiceUnavailable:
			return info("服务繁忙", "图片生成服务当前繁忙，请稍后重试。", "service_busy", publicServiceCategory, true, "retry_later", "请稍后重新提交", http.StatusServiceUnavailable)
		default:
			if upstream.StatusCode >= http.StatusInternalServerError {
				return info("服务暂时异常", "图片生成服务暂时异常，请稍后重试。", "service_error", publicServiceCategory, true, "retry_later", "请稍后重新提交", http.StatusBadGateway)
			}
		}
	}
	return fallback(statusHint)
}

func isSkippedMainlineText(text string) bool {
	var value struct {
		SkippedMainline bool `json:"skipped_mainline"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &value) != nil {
		return false
	}
	return value.SkippedMainline
}

func sanitizeAssistantText(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || containsPrivateErrorDetail(strings.ToLower(value)) {
		return ""
	}
	value = publicURLPattern.ReplaceAllString(value, "")
	value = publicEndpointPattern.ReplaceAllString(value, "")
	value = strings.TrimSpace(value)
	if len(value) > 1024 {
		return value[:1024] + "…"
	}
	return value
}

func classifyUpstreamError(upstream *openaiweb.UpstreamError) (Info, bool) {
	if upstream == nil || upstream.StatusCode <= 0 {
		return Info{}, false
	}

	fields, parsed := parseUpstreamError(upstream.Body)
	if !parsed {
		return Info{}, false
	}
	return classifyStructuredError(fields, upstream.StatusCode, upstream.Body), true
}

func classifyStructuredError(fields upstreamErrorFields, status int, fallbackBody string) Info {
	retryable := status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
	if fields.Retryable != nil {
		retryable = *fields.Retryable
	}

	category := normalizeUpstreamCategory(fields.Category)
	code := normalizePublicCode(fields.Code, status, category, retryable)
	title := sanitizePublicText(fields.Title)
	message := sanitizePublicText(fields.Message)
	hint := sanitizePublicText(fields.Hint)
	action := normalizePublicAction(fields.Action, retryable)
	if title == "" {
		if category == "request" {
			title = "请求处理失败"
		} else {
			title = "服务请求失败"
		}
	}
	if message == "" {
		message = sanitizePublicText(compactUpstreamBody(fallbackBody))
	}
	if message == "" {
		message = "服务暂时没有返回详细原因，请稍后重试。"
	}
	message = formatPublicMessage(message, hint)

	return info(title, message, code, category, retryable, action, hint, status)
}

func parseUpstreamError(body string) (upstreamErrorFields, bool) {
	trimmed := strings.TrimSpace(body)
	candidates := []string{trimmed}
	if start := strings.Index(trimmed, "{"); start > 0 {
		candidates = append(candidates, trimmed[start:])
	}
	for _, candidate := range candidates {
		var envelope upstreamErrorEnvelope
		if err := json.Unmarshal([]byte(candidate), &envelope); err != nil {
			continue
		}
		if envelope.Error != nil {
			return *envelope.Error, true
		}
		if strings.TrimSpace(envelope.Message) != "" || strings.TrimSpace(envelope.Code) != "" || strings.TrimSpace(envelope.Title) != "" {
			return upstreamErrorFields{Title: envelope.Title, Message: envelope.Message, Type: envelope.Type, Code: envelope.Code, Hint: envelope.Hint}, true
		}
	}
	return upstreamErrorFields{}, false
}

func normalizeUpstreamCategory(value string) string {
	switch strings.TrimSpace(value) {
	case "request", "policy", "capacity", "client", "account", "system", "canceled":
		return strings.TrimSpace(value)
	case "service", "upstream":
		return publicServiceCategory
	default:
		return publicServiceCategory
	}
}

func formatPublicMessage(message, hint string) string {
	parts := make([]string, 0, 2)
	if message != "" {
		parts = append(parts, message)
	}
	if hint != "" && !strings.EqualFold(hint, message) {
		parts = append(parts, "建议："+hint)
	}
	return strings.Join(parts, "；")
}

func compactUpstreamBody(body string) string {
	body = strings.Join(strings.Fields(strings.TrimSpace(body)), " ")
	if body == "" {
		return ""
	}
	lower := strings.ToLower(body)
	if containsPrivateErrorDetail(lower) || strings.Contains(lower, "provider error") || strings.Contains(lower, "upstream ") {
		return ""
	}
	const maxLength = 2048
	if len(body) > maxLength {
		return body[:maxLength] + "…"
	}
	return body
}

func normalizePublicCode(value string, status int, category string, retryable bool) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if publicCodePattern.MatchString(value) && !strings.Contains(value, "upstream") && !strings.Contains(value, "provider") && !strings.Contains(value, "http") {
		return value
	}
	if category == "request" {
		return "request_failed"
	}
	if status == http.StatusTooManyRequests {
		return "service_rate_limited"
	}
	if retryable || status >= http.StatusInternalServerError {
		return "service_error"
	}
	return "request_failed"
}

func normalizePublicAction(value string, retryable bool) string {
	switch strings.TrimSpace(value) {
	case "none", "check_request", "modify_content", "retry_request", "retry_later", "check_api_key", "wait_quota_reset":
		return strings.TrimSpace(value)
	default:
		if retryable {
			return "retry_later"
		}
		return "check_request"
	}
}

func sanitizePublicText(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || containsPrivateErrorDetail(strings.ToLower(value)) {
		return ""
	}
	value = publicURLPattern.ReplaceAllString(value, "")
	value = publicEndpointPattern.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "上游", "服务")
	value = strings.ReplaceAll(value, "Upstream", "服务")
	value = strings.ReplaceAll(value, "upstream", "服务")
	value = strings.ReplaceAll(value, "Provider", "服务")
	value = strings.ReplaceAll(value, "provider", "服务")
	value = strings.ReplaceAll(value, "OAI", "图片服务")
	value = strings.ReplaceAll(value, "oai", "图片服务")
	value = strings.TrimSpace(strings.Trim(value, "：:，,；;。.!！?？\"'()[]{}<>"))
	if value == "" {
		return ""
	}
	const maxLength = 1024
	if len(value) > maxLength {
		return value[:maxLength] + "…"
	}
	return value
}

func containsPrivateErrorDetail(lower string) bool {
	return strings.Contains(lower, "access_token") ||
		strings.Contains(lower, "refresh_token") ||
		strings.Contains(lower, "id_token") ||
		strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "bearer ") ||
		strings.Contains(lower, "oauth token") ||
		strings.Contains(lower, "/backend-api/") ||
		strings.Contains(lower, "/backend-anon/") ||
		strings.Contains(lower, "/v1/") ||
		strings.Contains(lower, "https://") ||
		strings.Contains(lower, "http://")
}

func ClassifyText(message string, statusHint int) Info {
	message = strings.TrimSpace(message)
	if assistantText, ok := persistedImageResponseText(message); ok {
		if openaiweb.IsNoFreeImageQuotaError(errors.New(assistantText)) {
			return Classify(errors.New(assistantText), statusHint)
		}
		return info("图片未生成", assistantText, "image_response_text", "request", false, "check_request", "", http.StatusBadRequest)
	}
	return Classify(errors.New(message), statusHint)
}

// persistedImageResponseText restores the upstream assistant message from a
// legacy task error. New tasks retain the typed error through Classify, while
// persisted tasks only have Error()'s textual representation available.
func persistedImageResponseText(message string) (string, bool) {
	const prefix = "image generation returned text without an image:"
	if !strings.HasPrefix(message, prefix) {
		return "", false
	}
	message = strings.TrimSpace(strings.TrimPrefix(message, prefix))
	if index := strings.LastIndex(message, "; conversation_id="); index >= 0 {
		message = strings.TrimSpace(message[:index])
	}
	return message, message != ""
}

func CategoryLabel(category string) string {
	switch strings.TrimSpace(category) {
	case "request":
		return "请求参数"
	case "policy":
		return "内容安全"
	case "capacity":
		return "服务容量"
	case "client":
		return "客户额度"
	case "account":
		return "账号状态"
	case "service", "upstream":
		return "图片服务"
	case "system":
		return "本地系统"
	case "canceled":
		return "用户取消"
	default:
		return "其他错误"
	}
}

func info(title, message, code, category string, retryable bool, action, hint string, status int) Info {
	errType := "server_error"
	switch category {
	case "request", "policy":
		errType = "invalid_request_error"
	case "capacity", "client":
		errType = "rate_limit_error"
	case "service", "upstream":
		errType = "service_error"
	case "canceled":
		errType = "request_canceled"
	}
	if strings.Contains(code, "timeout") {
		errType = "timeout_error"
	}
	return Info{Title: title, Message: message, Type: errType, Code: code, Category: category, Retryable: retryable, Action: action, Hint: hint, HTTPStatus: status}
}

func fallback(statusHint int) Info {
	switch statusHint {
	case http.StatusBadRequest:
		return info("请求参数不正确", "请求参数不正确，请检查后重新提交。", "invalid_request", "request", false, "check_request", "请检查请求参数", statusHint)
	case http.StatusUnauthorized:
		return info("身份验证失败", "API Key 无效或已失效。", "api_key_invalid", "request", false, "check_api_key", "请检查 API Key", statusHint)
	case http.StatusForbidden:
		return info("没有访问权限", "当前 API Key 没有此操作权限。", "request_not_allowed", "request", false, "check_api_key", "请检查 API Key 权限", statusHint)
	case http.StatusNotFound:
		return info("请求资源不存在", "请求的资源不存在。", "resource_not_found", "request", false, "check_request", "请检查请求地址", statusHint)
	case http.StatusTooManyRequests:
		return info("请求暂时受限", "当前请求较多，请稍后重试。", "request_rate_limited", "capacity", true, "retry_later", "请稍后重新提交", statusHint)
	case http.StatusInternalServerError:
		return info("服务内部异常", "服务内部处理失败，请稍后重试。", "internal_error", "system", true, "retry_later", "请稍后重新提交", statusHint)
	case http.StatusServiceUnavailable:
		return info("服务暂不可用", "服务暂时不可用，请稍后重试。", "service_unavailable", "system", true, "retry_later", "请稍后重新提交", statusHint)
	case http.StatusGatewayTimeout:
		return info("服务响应超时", "图片生成服务响应超时，请稍后重试。", "service_timeout", publicServiceCategory, true, "retry_later", "请稍后重新提交", statusHint)
	default:
		return info("服务暂时异常", "图片生成服务暂时异常，请稍后重试。", "service_error", publicServiceCategory, true, "retry_later", "请稍后重新提交", http.StatusBadGateway)
	}
}

func classifyQuota(quota *auth.QuotaError) Info {
	message := strings.ToLower(strings.TrimSpace(quota.Message))
	switch {
	case strings.Contains(message, "disabled"), strings.Contains(message, "no longer exists"):
		return info("API Key 不可用", "API Key 无效、已停用或已删除。", "api_key_invalid", "request", false, "check_api_key", "请检查 API Key", quota.StatusCode)
	case strings.Contains(message, "endpoint"):
		return info("接口权限不足", "当前 API Key 没有访问此接口的权限。", "endpoint_not_allowed", "request", false, "check_api_key", "请检查 API Key 权限", quota.StatusCode)
	case strings.Contains(message, "model"):
		return info("模型权限不足", "当前 API Key 没有使用此模型的权限。", "model_not_allowed", "request", false, "check_api_key", "请检查 API Key 模型权限", quota.StatusCode)
	default:
		return info("调用额度已用完", "当前 API Key 的调用额度已用完。", "client_quota_exhausted", "client", false, "wait_quota_reset", "请等待额度恢复", quota.StatusCode)
	}
}

func isContentPolicyText(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, "content policy violation") || strings.Contains(value, "防护限制") || strings.Contains(value, "可能违反")
}
