package openaiweb

import (
	"fmt"
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
	if IsAuthenticationError(&UpstreamError{Path: "/backend-api/me", StatusCode: 403, Body: "forbidden"}) {
		t.Fatal("non-401 upstream error must not be treated as credential failure")
	}
}
