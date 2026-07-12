package openaiweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"imagepool/internal/accounts"
)

type DebugRequest struct {
	Method         string            `json:"method"`
	Path           string            `json:"path"`
	AccessToken    string            `json:"access_token,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Body           any               `json:"body,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Bootstrap      bool              `json:"bootstrap"`
}

type DebugResponse struct {
	OK              bool              `json:"ok"`
	Status          int               `json:"status"`
	ElapsedMS       float64           `json:"elapsed_ms"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	RequestHeaders  map[string]any    `json:"request_headers"`
	ResponseHeaders map[string]string `json:"response_headers"`
	Body            any               `json:"body"`
}

func (c *Client) Debug(ctx context.Context, input DebugRequest) (DebugResponse, error) {
	if c == nil {
		return DebugResponse{}, fmt.Errorf("web client is not configured")
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return DebugResponse{}, fmt.Errorf("method must be GET, POST, PUT, PATCH, or DELETE")
	}
	path, err := c.debugPath(input.Path)
	if err != nil {
		return DebugResponse{}, err
	}
	timeout := input.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 120 {
		timeout = 120
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	account := accounts.Account{AccessToken: strings.TrimSpace(input.AccessToken)}
	if input.Bootstrap {
		if err := c.bootstrap(ctx, account); err != nil {
			return DebugResponse{}, err
		}
	}
	var body io.Reader
	if input.Body != nil {
		encoded, err := json.Marshal(input.Body)
		if err != nil {
			return DebugResponse{}, fmt.Errorf("encode debug body: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return DebugResponse{}, err
	}
	headers := c.headers(account, path, path, map[string]string{})
	if input.Body != nil {
		headers["Content-Type"] = "application/json"
	}
	for key, value := range input.Headers {
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key != "" {
			headers[key] = value
		}
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	started := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DebugResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<10))
	if err != nil {
		return DebugResponse{}, err
	}
	result := DebugResponse{
		OK:        resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest,
		Status:    resp.StatusCode,
		ElapsedMS: float64(time.Since(started).Microseconds()) / 1000,
		Method:    method,
		URL:       c.baseURL + path,
		RequestHeaders: map[string]any{
			"X-OpenAI-Target-Path":  headers["X-OpenAI-Target-Path"],
			"X-OpenAI-Target-Route": headers["X-OpenAI-Target-Route"],
			"Authorization":         map[bool]any{true: "Bearer ***", false: nil}[account.AccessToken != ""],
		},
		ResponseHeaders: debugResponseHeaders(resp.Header),
		Body:            debugBody(raw),
	}
	return result, nil
}

func (c *Client) debugPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() {
		base, baseErr := url.Parse(c.baseURL)
		if baseErr != nil || !strings.EqualFold(parsed.Host, base.Host) {
			return "", fmt.Errorf("only configured ChatGPT Web URLs are allowed")
		}
		value = parsed.EscapedPath()
		if parsed.RawQuery != "" {
			value += "?" + parsed.RawQuery
		}
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if !strings.HasPrefix(value, "/backend-api/") && !strings.HasPrefix(value, "/backend-anon/") {
		return "", fmt.Errorf("path must start with /backend-api/ or /backend-anon/")
	}
	return value, nil
}

func debugBody(raw []byte) any {
	var body any
	if json.Unmarshal(raw, &body) == nil {
		return body
	}
	return string(raw)
}

func debugResponseHeaders(headers http.Header) map[string]string {
	allowed := map[string]bool{"content-type": true, "date": true, "server": true, "cf-ray": true, "openai-version": true}
	out := map[string]string{}
	for key, values := range headers {
		if allowed[strings.ToLower(key)] {
			out[key] = strings.Join(values, ", ")
		}
	}
	return out
}
