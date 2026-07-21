package images

import (
	"encoding/json"
	"strings"
	"testing"

	"imagepool/internal/openaiweb"
)

func TestMarshalForOpenAIRedactsUpstreamAttemptDiagnostics(t *testing.T) {
	raw := (&openaiweb.UpstreamError{
		Path:       "/backend-api/files",
		StatusCode: 401,
		Body:       `{"error":{"code":"token_revoked","message":"invalidated oauth token"}}`,
	}).Error()
	payload := (Response{Attempts: []openaiweb.AttemptLog{{Attempt: 1, Status: "failed", Error: raw}}}).MarshalForOpenAI()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(body))
	for _, leaked := range []string{"/backend-api/", "token_revoked", "invalidated oauth token", "body="} {
		if strings.Contains(text, leaked) {
			t.Fatalf("payload leaked %q: %s", leaked, body)
		}
	}
	if !strings.Contains(string(body), openaiweb.PublicCredentialInvalidMessage) {
		t.Fatalf("payload=%s", body)
	}
}

func TestMarshalForOpenAIHidesPollTimeoutDetails(t *testing.T) {
	raw := "image poll timeout: ChatGPT 生图任务已等待 300 秒"
	payload := (Response{Attempts: []openaiweb.AttemptLog{{Attempt: 1, Status: "failed", Error: raw}}}).MarshalForOpenAI()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(strings.ToLower(text), "image poll timeout") || strings.Contains(text, "生图任务已等待") {
		t.Fatalf("payload leaked poll timeout: %s", body)
	}
	if !strings.Contains(text, openaiweb.PublicImagePollTimeoutMessage) {
		t.Fatalf("payload=%s", body)
	}
}
