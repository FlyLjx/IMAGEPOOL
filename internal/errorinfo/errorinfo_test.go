package errorinfo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"imagepool/internal/accounts"
	"imagepool/internal/openaiweb"
)

func TestClassifyImageErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "generation terminated", err: errors.Join(openaiweb.ErrImageGenerationTerminated, errors.New("image_generation_failed")), code: "image_generation_failed", status: http.StatusBadGateway},
		{name: "poll timeout", err: openaiweb.ErrPollTimeout, code: "oai_image_generation_timeout", status: http.StatusTooManyRequests},
		{name: "legacy poll timeout", err: errors.New("任务占用额度失败，请再次提交。"), code: "oai_image_generation_timeout", status: http.StatusTooManyRequests},
		{name: "upload timeout", err: errors.Join(openaiweb.ErrImagePreparationTimeout, errors.New("参考图上传超时")), code: "image_upload_timeout", status: http.StatusGatewayTimeout},
		{name: "no account", err: accounts.ErrNoAvailableAccount, code: "account_pool_unavailable", status: http.StatusTooManyRequests},
		{name: "revoked credential", err: &openaiweb.UpstreamError{Path: "/backend-api/files", StatusCode: http.StatusUnauthorized, Body: `{"code":"token_revoked"}`}, code: "account_pool_unavailable", status: http.StatusTooManyRequests},
		{name: "legacy credential message", err: errors.New(openaiweb.PublicCredentialInvalidMessage), code: "account_pool_unavailable", status: http.StatusTooManyRequests},
		{name: "content policy", err: openaiweb.ErrContentPolicy, code: "content_policy_violation", status: http.StatusBadRequest},
		{name: "canceled", err: context.Canceled, code: "request_canceled", status: StatusClientClosedRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Classify(test.err, 0)
			if got.Code != test.code || got.HTTPStatus != test.status {
				t.Fatalf("info=%#v", got)
			}
			if got.Message == "" || got.Title == "" || got.Category == "" || got.Action == "" {
				t.Fatalf("incomplete info=%#v", got)
			}
		})
	}
}

func TestClassifyDoesNotExposeAccountAttempts(t *testing.T) {
	got := Classify(&openaiweb.UpstreamError{Path: "/backend-api/files", StatusCode: http.StatusUnauthorized, Body: `{"code":"token_revoked"}`}, 0)
	for _, forbidden := range []string{"账号", "切换", "删除", "1/", "token"} {
		if strings.Contains(strings.ToLower(got.Message+got.Hint), strings.ToLower(forbidden)) {
			t.Fatalf("public info exposed %q: %#v", forbidden, got)
		}
	}
}
