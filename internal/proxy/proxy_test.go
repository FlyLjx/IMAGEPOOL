package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"imagepool/internal/config"
)

func TestNormalizeAndValidateURL(t *testing.T) {
	if got := NormalizeURL("socks5://127.0.0.1:1080"); got != "socks5h://127.0.0.1:1080" {
		t.Fatalf("normalized=%q", got)
	}
	if err := ValidateURL("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}

func TestHTTPProxyTransport(t *testing.T) {
	seen := ""
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()
	runtime := config.ProxyRuntime{Enabled: true, EgressMode: "single_proxy", ProxyURL: proxyServer.URL}
	client, err := NewHTTPClientForRuntime(runtime, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("http://example.test/through-proxy")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || seen != "http://example.test/through-proxy" {
		t.Fatalf("status=%d seen=%q", response.StatusCode, seen)
	}
}

func TestProxyTestTreatsForbiddenResponseAsFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer upstream.Close()

	result := Test(t.Context(), config.ProxyRuntime{Enabled: true, EgressMode: "direct"}, upstream.URL, time.Second)
	if ok, _ := result["ok"].(bool); ok {
		t.Fatalf("forbidden response reported as success: %#v", result)
	}
	if status, _ := result["status"].(int); status != http.StatusForbidden {
		t.Fatalf("status=%v result=%#v", result["status"], result)
	}
}
