package openaiweb

import (
	"fmt"
	"strings"
	"testing"
)

func TestInteractiveChallengeError(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("chat requirements requires turnstile token"),
		fmt.Errorf("chat requirements requires arkose token"),
	} {
		if !IsInteractiveChallengeError(err) {
			t.Fatalf("expected interactive challenge: %v", err)
		}
	}
	if IsInteractiveChallengeError(fmt.Errorf("image generation failed")) {
		t.Fatal("ordinary image failure must not be an interactive challenge")
	}
}

func TestTokenInvalidErrorRequiresExplicitRevocation(t *testing.T) {
	if IsTokenInvalidError(fmt.Errorf("upstream /backend-api/me status=401 body=unauthorized bearer request")) {
		t.Fatal("generic unauthorized response must not be treated as token revocation")
	}
	if !IsTokenInvalidError(fmt.Errorf("token_revoked")) {
		t.Fatal("explicit token_revoked response must be treated as invalid")
	}
}

func TestAuthenticationErrorIncludesGenericUpstream401(t *testing.T) {
	if !IsAuthenticationError(&UpstreamError{Path: "/backend-api/me", StatusCode: 401, Body: "unauthorized"}) {
		t.Fatal("upstream 401 must be eligible for credential recovery")
	}
	if IsAuthenticationError(fmt.Errorf("prepare conversation(success): %w", ErrMissingConduitToken)) {
		t.Fatal("empty conduit token is not credential revocation")
	}
	if IsAuthenticationError(&UpstreamError{Path: "/backend-api/me", StatusCode: 403, Body: "forbidden"}) {
		t.Fatal("non-401 upstream error must not be treated as credential failure")
	}
}

func TestNoFreeImageQuotaErrorIncludesFreePlanImageGenerationLimit(t *testing.T) {
	err := fmt.Errorf("You've hit the Free plan limit for image generations requests")
	if !IsNoFreeImageQuotaError(err) {
		t.Fatalf("free-plan image generation limit must be classified as quota exhaustion: %v", err)
	}
}

func TestImageTimeoutRetryClassification(t *testing.T) {
	if !IsRetryableImageError(fmt.Errorf("poll failed: %w", ErrPollTimeout)) {
		t.Fatal("pre-conversation poll timeout must switch to another account")
	}
	if IsRetryableImageError(NewImageConversationTimeoutError("conv-1", 300)) {
		t.Fatal("accepted conversation timeout must not switch to another account")
	}
	if !IsImageConversationTimeout(fmt.Errorf("polling failed: %w", NewImageConversationTimeoutError("conv-1", 300))) {
		t.Fatal("accepted conversation timeout must remain identifiable through wrapping")
	}
	if !IsRetryableImageError(fmt.Errorf("prepare failed: %w", ErrImagePreparationTimeout)) {
		t.Fatal("preparation timeout must switch accounts")
	}
	if !IsRetryableImageError(fmt.Errorf("tool failed: %w", ErrImageGenerationTerminated)) {
		t.Fatal("terminal image-tool status must switch accounts")
	}
	if !IsRetryableImageError(fmt.Errorf("prepare conversation(none): %w", ErrMissingConduitToken)) {
		t.Fatal("missing conduit token must switch accounts")
	}
	if !IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"skipped_mainline":true}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("skipped_mainline must switch accounts")
	}
	if !IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"size":"1024x1024","n":1,"referenced_image_ids":["file_a","file_b"],"prompt":null}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("referenced_image_ids with null prompt must switch accounts")
	}
	if !IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"referenced_image_ids":["file_a"],"prompt":"draw this"}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("referenced_image_ids with an echoed prompt must switch accounts")
	}
	if IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"prompt":null}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("a null prompt without reference IDs must not switch accounts")
	}
	if !IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"referenced_image_ids":["file_a"]}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("referenced_image_ids without a generated image must switch accounts")
	}
	if !IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"size":"1792x1024","n":1,"prompt":"draw a thumbnail"}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("echoed image request parameters must switch accounts")
	}
	if IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"prompt":"draw a thumbnail"}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("a prompt alone must not be treated as an echoed image request")
	}
	if IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"size":"1792x1024","prompt":null}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("a null prompt without reference IDs must not switch accounts")
	}
	if !IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"size":"1024x1536","n":1,"prompt":null,"is_style_transfer":false,"referenced_image_ids":null,"transparent_background":false}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("a complete image request schema with a null prompt must switch accounts")
	}
	if IsRetryableImageError(NewImageAssistantTextError("conv-1", `{"size":"1024x1536","prompt":null,"transparent_background":false}`, ImageAttemptDiagnostics{}, false)) {
		t.Fatal("a partial object with a null prompt must not be treated as an image request echo")
	}
	if IsRetryableImageError(NewImageAssistantTextError("conv-1", "请上传图片后继续。", ImageAttemptDiagnostics{}, false)) {
		t.Fatal("ordinary assistant text must not switch accounts")
	}
}

func TestRetryableImageErrorIncludesTransientUpstreamStatuses(t *testing.T) {
	for _, status := range []int{408, 409, 425, 429, 500, 502, 503, 504} {
		if !IsRetryableImageError(&UpstreamError{StatusCode: status}) {
			t.Fatalf("status %d must be retryable", status)
		}
	}
	if IsRetryableImageError(&UpstreamError{StatusCode: 400}) {
		t.Fatal("400 must not be retryable")
	}
}

func TestImageReferenceUploadRateLimitClassification(t *testing.T) {
	raw := &UpstreamError{Path: "/backend-api/files", StatusCode: 429, Body: "reference upload quota"}
	if !IsImageReferenceUploadRateLimited(&imageUploadStageError{Stage: "create", Err: raw}) {
		t.Fatal("reference upload 429 must be capability-scoped")
	}
	if !IsImageReferenceUploadRateLimited(fmt.Errorf("wrapped: %w", &imageUploadStageError{Stage: "confirm", Err: raw})) {
		t.Fatal("wrapped reference upload 429 must remain detectable")
	}
	if IsImageReferenceUploadRateLimited(raw) {
		t.Fatal("generation or direct 429 must not be classified as reference upload 429")
	}
}

func TestPublicErrorProjectionRedactsCredentialDiagnostics(t *testing.T) {
	raw := &UpstreamError{
		Path:       "/backend-api/files",
		StatusCode: 401,
		Body:       `{"error":{"code":"token_revoked","message":"invalidated oauth token"}}`,
	}
	if !IsAuthenticationError(raw) {
		t.Fatal("raw upstream error must remain usable for credential handling")
	}
	message := PublicErrorMessage(raw)
	if message != PublicCredentialInvalidMessage {
		t.Fatalf("message=%q", message)
	}
	for _, leaked := range []string{"/backend-api/", "token_revoked", "oauth token", raw.Body} {
		if strings.Contains(strings.ToLower(message), strings.ToLower(leaked)) {
			t.Fatalf("public message leaked %q: %q", leaked, message)
		}
	}

	attempts := []AttemptLog{{Attempt: 1, Status: "failed", Error: raw.Error()}}
	publicAttempts := PublicAttemptLogs(attempts)
	if publicAttempts[0].Error != PublicCredentialInvalidMessage {
		t.Fatalf("attempts=%#v", publicAttempts)
	}
	if !strings.Contains(attempts[0].Error, "token_revoked") {
		t.Fatalf("source attempts were unexpectedly changed: %#v", attempts)
	}

	event := PublicProgressEvent(ProgressEvent{Message: raw.Error(), Details: map[string]any{"error": raw.Error(), "nested": map[string]any{"cause": raw.Error()}}})
	if event.Message != PublicCredentialInvalidMessage || event.Details["error"] != PublicCredentialInvalidMessage {
		t.Fatalf("event=%#v", event)
	}
	nested, _ := event.Details["nested"].(map[string]any)
	if nested["cause"] != PublicCredentialInvalidMessage {
		t.Fatalf("nested details=%#v", event.Details)
	}
}

func TestPublicErrorTextPreservesAssistantTextForBackend(t *testing.T) {
	message := `image generation returned text without an image: 非常抱歉，该提示可能违反了内容政策。; conversation_id=conv-1`
	got := PublicErrorText(message)
	want := `非常抱歉，该提示可能违反了内容政策。`
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestPublicErrorTextHidesSkippedMainlineMarker(t *testing.T) {
	got := PublicErrorText(`image generation returned text without an image: {"skipped_mainline":true}; conversation_id=conv-1`)
	if got != "本次请求未触发生图流程，请修改提示词后重新提交。" {
		t.Fatalf("message=%q", got)
	}
}
