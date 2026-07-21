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
