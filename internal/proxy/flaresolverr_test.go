package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"imagepool/internal/config"
)

func TestSolveFlareSolverrParsesCookieBundle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" || r.Method != http.MethodPost {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","solution":{"userAgent":"solver-agent","cookies":[{"name":"cf_clearance","value":"clear"},{"name":"other","value":"value"}]}}`))
	}))
	defer srv.Close()
	runtime := config.ProxyRuntime{Enabled: true, EgressMode: "direct", Clearance: config.ClearanceRuntime{Enabled: true, Mode: "flaresolverr", FlareSolverrURL: srv.URL, TimeoutSec: 5}}
	result, err := SolveFlareSolverr(context.Background(), runtime, "https://chatgpt.com")
	if err != nil {
		t.Fatal(err)
	}
	if result.Clearance != "clear" || result.UserAgent != "solver-agent" || result.Cookies != "cf_clearance=clear; other=value" {
		t.Fatalf("result=%#v", result)
	}
}
