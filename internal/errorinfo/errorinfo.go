package errorinfo

import (
	"context"
	"errors"
	"net/http"
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

func Classify(err error, statusHint int) Info {
	if err == nil {
		return fallback(statusHint)
	}
	text := strings.TrimSpace(err.Error())
	lower := strings.ToLower(text)

	if errors.Is(err, context.Canceled) {
		return info("任务已取消", "请求已取消。", "request_canceled", "canceled", false, "none", "", StatusClientClosedRequest)
	}
	if errors.Is(err, openaiweb.ErrContentPolicy) || isContentPolicyText(text) {
		return info("内容安全限制", "提交内容触发了安全限制，请调整提示词或参考图后重试。", "content_policy_violation", "policy", false, "modify_content", "调整提示词或参考图后重新提交", http.StatusBadRequest)
	}
	if errors.Is(err, openaiweb.ErrImageGenerationTerminated) || strings.Contains(lower, "image_generation_failed") || strings.Contains(lower, "image generation failed") {
		return info("OAI 未完成生图", "OAI 未能完成本次图片生成，系统已自动重试，请重新提交。", "image_generation_failed", "upstream", true, "retry_request", "请直接重新提交任务", http.StatusBadGateway)
	}
	if errors.Is(err, openaiweb.ErrPollTimeout) || strings.Contains(lower, "image poll timeout") || strings.Contains(text, "生图任务已等待") || strings.Contains(text, "OAI侧出图超出") || strings.Contains(text, "任务占用额度失败") {
		return info("OAI 生图超时", "OAI 在 300 秒内未完成出图，本次任务已结束，请重新提交。", "oai_image_generation_timeout", "upstream", true, "retry_request", "请重新提交任务", http.StatusTooManyRequests)
	}
	if errors.Is(err, openaiweb.ErrImagePreparationTimeout) {
		if strings.Contains(text, "参考图") || strings.Contains(lower, "upload") {
			return info("参考图上传超时", "参考图上传暂时超时，请重新提交。", "image_upload_timeout", "upstream", true, "retry_request", "请重新提交任务", http.StatusGatewayTimeout)
		}
		return info("生图会话准备超时", "OAI 生图会话准备超时，请重新提交。", "image_preparation_timeout", "upstream", true, "retry_request", "请重新提交任务", http.StatusGatewayTimeout)
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "context deadline exceeded") {
		return info("上游响应超时", "上游服务未在规定时间内响应，请稍后重试。", "upstream_timeout", "upstream", true, "retry_later", "稍后重新提交任务", http.StatusGatewayTimeout)
	}
	if errors.Is(err, openaiweb.ErrMissingConduitToken) || strings.Contains(lower, "missing conduit_token") || strings.Contains(lower, "conversation_id not found") {
		return info("生图会话建立失败", "OAI 生图会话暂时无法建立，系统已自动重试，请重新提交。", "image_session_failed", "upstream", true, "retry_request", "请重新提交任务", http.StatusBadGateway)
	}
	if openaiweb.IsInteractiveChallengeError(err) {
		return info("OAI 要求人机验证", "OAI 当前要求完成人机验证，服务暂时不可用。", "interactive_challenge_required", "upstream", true, "retry_later", "请稍后重试", http.StatusPreconditionRequired)
	}
	if openaiweb.IsNoFreeImageQuotaError(err) {
		return info("号池额度不足", "当前号池生图额度不足，请稍后重试。", "image_quota_exhausted", "capacity", true, "retry_later", "等待号池补充额度后重试", http.StatusTooManyRequests)
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
		return info("生图结果暂不可用", "OAI 已结束本次任务，但没有返回可用图片，请重新提交。", "image_result_unavailable", "upstream", true, "retry_request", "请重新提交任务", http.StatusBadGateway)
	}

	var upstream *openaiweb.UpstreamError
	if errors.As(err, &upstream) {
		if strings.Contains(strings.ToLower(upstream.Path), "/files") {
			return info("参考图上传失败", "OAI 参考图上传服务暂时不可用，请重新提交。", "image_upload_failed", "upstream", true, "retry_request", "请重新提交任务", http.StatusBadGateway)
		}
		switch upstream.StatusCode {
		case http.StatusTooManyRequests:
			return info("OAI 请求受限", "OAI 当前请求频率受限，请稍后重试。", "upstream_rate_limited", "upstream", true, "retry_later", "请稍后重新提交", http.StatusTooManyRequests)
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return info("上游响应超时", "OAI 服务响应超时，请稍后重试。", "upstream_timeout", "upstream", true, "retry_later", "请稍后重新提交", http.StatusGatewayTimeout)
		case http.StatusServiceUnavailable:
			return info("OAI 服务繁忙", "OAI 服务当前繁忙，请稍后重试。", "upstream_service_busy", "upstream", true, "retry_later", "请稍后重新提交", http.StatusServiceUnavailable)
		default:
			if upstream.StatusCode >= http.StatusInternalServerError {
				return info("OAI 服务异常", "OAI 服务暂时异常，请稍后重试。", "upstream_service_error", "upstream", true, "retry_later", "请稍后重新提交", http.StatusBadGateway)
			}
		}
	}
	return fallback(statusHint)
}

func ClassifyText(message string, statusHint int) Info {
	return Classify(errors.New(strings.TrimSpace(message)), statusHint)
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
	case "upstream":
		return "OAI 上游"
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
	case "upstream":
		errType = "upstream_error"
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
		return info("上游响应超时", "上游服务响应超时，请稍后重试。", "upstream_timeout", "upstream", true, "retry_later", "请稍后重新提交", statusHint)
	default:
		return info("上游服务异常", "上游服务暂时异常，请稍后重试。", "upstream_service_error", "upstream", true, "retry_later", "请稍后重新提交", http.StatusBadGateway)
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
