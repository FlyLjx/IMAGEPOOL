package adobe

import (
	"net/http"
	"testing"
)

func TestAdobeTimeoutIsRetryableWithoutARP(t *testing.T) {
	err := classifyAdobeHTTPError("image submission", &http.Response{StatusCode: http.StatusRequestTimeout, Header: make(http.Header)}, []byte(`{"error":"timeout_error"}`))
	upstream, ok := err.(*upstreamError)
	if !ok || upstream.Kind != "timeout" || !upstream.Retryable || upstream.Message == "" {
		t.Fatalf("error=%#v", err)
	}
}

func TestAdobeGenerationHeadersMatchAdobe2APIWithoutARP(t *testing.T) {
	request, _ := http.NewRequest(http.MethodPost, imageSubmitURL, nil)
	applyAdobeSubmitHeaders(request, SessionSnapshot{AccessToken: "token", UserAgent: "ua"}, "prompt", "png")
	if request.Header.Get("x-api-key") != adobe2APIKey || request.Header.Get("Origin") != "https://new.express.adobe.com" || request.Header.Get("x-arp-session-id") != "" || request.Header.Get("x-nonce") == "" || request.Header.Get("x-accept-mimetype") != "image/png" {
		t.Fatalf("headers=%v", request.Header)
	}
}

func TestNormalizeAdobeOutputFormat(t *testing.T) {
	tests := map[string]string{"": "png", "auto": "png", "png": "png", "jpg": "jpeg", "jpeg": "jpeg", "webp": "webp"}
	for input, expected := range tests {
		actual, err := normalizeAdobeOutputFormat(input)
		if err != nil || actual != expected {
			t.Fatalf("normalizeAdobeOutputFormat(%q)=%q, %v; want %q", input, actual, err, expected)
		}
	}
	if _, err := normalizeAdobeOutputFormat("gif"); err == nil {
		t.Fatal("expected unsupported Adobe output format to fail")
	}
}

func TestAdobeRouteProxyRuntimeSupportsDirect(t *testing.T) {
	direct := adobeRouteProxyRuntime(directRouteURL)
	if !direct.Enabled || direct.EgressMode != "direct" || direct.ProxyURL != "" {
		t.Fatalf("direct runtime=%#v", direct)
	}
	proxied := adobeRouteProxyRuntime("http://proxy.example:8080")
	if proxied.EgressMode != "single_proxy" || proxied.ProxyURL == "" || proxied.ResourceProxyURL == "" {
		t.Fatalf("proxied runtime=%#v", proxied)
	}
}
