package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"

	"imagepool/internal/config"
)

func NormalizeURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "socks://") {
		return "socks5h://" + value[len("socks://"):]
	}
	if strings.HasPrefix(lower, "socks5://") {
		return "socks5h://" + value[len("socks5://"):]
	}
	return value
}

func ValidateURL(value string) error {
	value = NormalizeURL(value)
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid proxy url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return nil
	default:
		return fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
}

func EffectiveURL(runtime config.ProxyRuntime, resource bool) string {
	if !runtime.Enabled || runtime.EgressMode != "single_proxy" {
		return ""
	}
	if resource && strings.TrimSpace(runtime.ResourceProxyURL) != "" {
		return NormalizeURL(runtime.ResourceProxyURL)
	}
	return NormalizeURL(runtime.ProxyURL)
}

func NewHTTPClient(cfg config.Config) (*http.Client, error) {
	return NewHTTPClientForRuntime(cfg.ProxyRuntime, time.Duration(cfg.RequestTimeoutSecs*float64(time.Second)))
}

func NewHTTPClientForRuntime(runtime config.ProxyRuntime, timeout time.Duration) (*http.Client, error) {
	return newHTTPClient(runtime, timeout, false)
}

func NewResourceHTTPClientForRuntime(runtime config.ProxyRuntime, timeout time.Duration) (*http.Client, error) {
	return newHTTPClient(runtime, timeout, true)
}

func newHTTPClient(runtime config.ProxyRuntime, timeout time.Duration, resource bool) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: runtime.SkipSSLVerify},
	}
	proxyURL := EffectiveURL(runtime, resource)
	if proxyURL == "" {
		return &http.Client{Timeout: timeout, Transport: transport}, nil
	}
	if err := ValidateURL(proxyURL); err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(proxyURL)
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(parsed)
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if parsed.User != nil {
			password, _ := parsed.User.Password()
			auth = &xproxy.Auth{User: parsed.User.Username(), Password: password}
		}
		dialer, err := xproxy.SOCKS5("tcp", parsed.Host, auth, xproxy.Direct)
		if err != nil {
			return nil, err
		}
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.Dial(network, address)
		}
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

func RuntimeStatus(runtime config.ProxyRuntime) map[string]any {
	proxyURL := EffectiveURL(runtime, false)
	clearance := runtime.Clearance
	return map[string]any{
		"enabled":                runtime.Enabled,
		"egress_mode":            runtime.EgressMode,
		"proxy_source":           proxySource(runtime),
		"has_proxy":              proxyURL != "",
		"clearance_enabled":      runtime.Enabled && clearance.Enabled && clearance.Mode != "none",
		"clearance_mode":         clearance.Mode,
		"has_clearance_bundle":   clearance.CFCookies != "" || clearance.CFClearance != "",
		"cached_clearance_hosts": []string{},
	}
}

func PublicRuntime(runtime config.ProxyRuntime) map[string]any {
	return map[string]any{
		"enabled": runtime.Enabled, "egress_mode": runtime.EgressMode, "proxy_url": runtime.ProxyURL, "resource_proxy_url": runtime.ResourceProxyURL,
		"skip_ssl_verify": runtime.SkipSSLVerify, "reset_session_status_codes": runtime.ResetSessionStatusCodes,
		"clearance": map[string]any{"enabled": runtime.Clearance.Enabled, "mode": runtime.Clearance.Mode, "cf_cookies": "", "cf_clearance": "", "has_cf_cookies": runtime.Clearance.CFCookies != "", "has_cf_clearance": runtime.Clearance.CFClearance != "", "user_agent": runtime.Clearance.UserAgent, "browser": runtime.Clearance.Browser, "flaresolverr_url": runtime.Clearance.FlareSolverrURL, "timeout_sec": runtime.Clearance.TimeoutSec, "refresh_interval": runtime.Clearance.RefreshInterval, "warm_up_on_start": runtime.Clearance.WarmUpOnStart},
	}
}

func Test(ctx context.Context, runtime config.ProxyRuntime, target string, timeout time.Duration) map[string]any {
	target = strings.TrimSpace(target)
	if target == "" {
		target = "https://chatgpt.com/api/auth/csrf"
	}
	client, err := NewHTTPClientForRuntime(runtime, timeout)
	if err != nil {
		return proxyTestResult(runtime, map[string]any{"ok": false, "status": 0, "latency_ms": 0, "url": target, "error": err.Error()})
	}
	return TestWithClient(ctx, client, runtime, target)
}

func TestWithClient(ctx context.Context, client *http.Client, runtime config.ProxyRuntime, target string) map[string]any {
	target = strings.TrimSpace(target)
	if target == "" {
		target = "https://chatgpt.com/api/auth/csrf"
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return proxyTestResult(runtime, map[string]any{"ok": false, "status": 0, "latency_ms": 0, "url": target, "error": err.Error()})
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (IMAGE POOL proxy test)")
	response, err := client.Do(req)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return proxyTestResult(runtime, map[string]any{"ok": false, "status": 0, "latency_ms": latency, "url": target, "error": RedactCredentials(err.Error())})
	}
	response.Body.Close()
	ok := response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusBadRequest
	errorText := ""
	if !ok {
		errorText = fmt.Sprintf("HTTP %d", response.StatusCode)
	}
	return proxyTestResult(runtime, map[string]any{"ok": ok, "status": response.StatusCode, "latency_ms": latency, "url": target, "error": errorText})
}

func proxyTestResult(runtime config.ProxyRuntime, result map[string]any) map[string]any {
	status := RuntimeStatus(runtime)
	result["runtime"] = status
	result["proxy_source"] = status["proxy_source"]
	result["has_proxy"] = status["has_proxy"]
	return result
}

func RedactCredentials(value string) string {
	parsed, err := url.Parse(value)
	if err == nil && parsed.User != nil {
		parsed.User = url.User("REDACTED")
		return parsed.String()
	}
	return value
}

func proxySource(runtime config.ProxyRuntime) string {
	if EffectiveURL(runtime, false) == "" {
		return "direct"
	}
	return "runtime"
}
