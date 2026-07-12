package oauthlogin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultYYDSMailAPIBase = "https://maliapi.215.im/v1"

// EmailOTPReader fetches the one-time verification code for a mailbox that
// belongs to the account being recovered.
type EmailOTPReader interface {
	ReadVerificationCode(context.Context, string) (string, error)
}

// YYDSMailReader consumes unread verification emails through the YYDS Mail
// API. The API key stays in runtime configuration and is never persisted in
// account records or recovery logs.
type YYDSMailReader struct {
	apiBase string
	apiKey  string
	client  *http.Client
}

func NewYYDSMailReader(apiBase, apiKey string, client *http.Client) *YYDSMailReader {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = defaultYYDSMailAPIBase
	}
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	return &YYDSMailReader{apiBase: apiBase, apiKey: strings.TrimSpace(apiKey), client: client}
}

// ReadVerificationCode waits for the next unread message in the requested
// mailbox. YYDS atomically marks messages as read, so unrelated messages are
// discarded and polling continues until a message with verificationCode lands.
func (r *YYDSMailReader) ReadVerificationCode(ctx context.Context, address string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("YYDS Mail reader is not configured")
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return "", fmt.Errorf("mailbox address is required")
	}
	if strings.TrimSpace(r.apiKey) == "" {
		return "", fmt.Errorf("YYDS Mail API key is not configured")
	}
	if r.client == nil {
		return "", fmt.Errorf("YYDS Mail HTTP client is not configured")
	}

	for {
		requestURL, err := url.Parse(r.apiBase + "/messages/next")
		if err != nil {
			return "", fmt.Errorf("build YYDS Mail request: %w", err)
		}
		query := requestURL.Query()
		query.Set("address", address)
		query.Set("wait", "30")
		requestURL.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-API-Key", r.apiKey)
		resp, err := r.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("read YYDS Mail verification code: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return "", fmt.Errorf("read YYDS Mail response: %w", readErr)
		}
		if resp.StatusCode == http.StatusNoContent {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return "", yydsMailHTTPError(resp.StatusCode, raw)
		}
		code := yydsVerificationCode(raw)
		if code != "" {
			return code, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
}

func yydsVerificationCode(raw []byte) string {
	var payload struct {
		Data struct {
			Message map[string]any `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"verificationCode", "verification_code"} {
		value, found := payload.Data.Message[key]
		if !found || value == nil {
			continue
		}
		code := strings.TrimSpace(fmt.Sprint(value))
		if code != "" && code != "<nil>" {
			return code
		}
	}
	return ""
}

func yydsMailHTTPError(status int, raw []byte) error {
	var payload struct {
		Error     string `json:"error"`
		ErrorCode string `json:"errorCode"`
	}
	_ = json.Unmarshal(raw, &payload)
	message := strings.TrimSpace(payload.Error)
	if code := strings.TrimSpace(payload.ErrorCode); code != "" {
		if message == "" {
			message = code
		} else {
			message = code + ": " + message
		}
	}
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	if len(message) > 500 {
		message = message[:500] + "..."
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("YYDS Mail request rejected (HTTP %d): %s", status, message)
}
