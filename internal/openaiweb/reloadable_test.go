package openaiweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
)

func TestReloadableClientAppliesNewConfig(t *testing.T) {
	first := config.Default()
	first.ChatGPTBaseURL = "https://one.example"
	first.ImageWebModelSlug = "gpt-5-5"
	client := NewReloadableClient(first)
	if got := client.snapshot().baseURL; got != "https://one.example" {
		t.Fatalf("base url=%q", got)
	}
	next := first
	next.ChatGPTBaseURL = "https://two.example"
	next.ImageWebModelSlug = "gpt-5-6"
	client.UpdateConfig(next)
	if got := client.snapshot().baseURL; got != "https://two.example" {
		t.Fatalf("base url=%q", got)
	}
	if got := client.snapshot().imageModelSlug; got != "gpt-5-6" {
		t.Fatalf("image slug=%q", got)
	}
}

func TestReloadableClientRefreshesFlareSolverrOnceForBootstrap403Burst(t *testing.T) {
	var appHits atomic.Int32
	var solverHits atomic.Int32
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		appHits.Add(1)
		if r.Header.Get("Cookie") != "cf_clearance=clear" {
			http.Error(w, "Cloudflare challenge", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("<html></html>"))
	}))
	defer app.Close()
	solver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1" {
			http.NotFound(w, r)
			return
		}
		solverHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"userAgent": "solver-agent",
				"cookies":   []any{map[string]any{"name": "cf_clearance", "value": "clear"}},
			},
		})
	}))
	defer solver.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = app.URL
	cfg.ProxyRuntime.Enabled = true
	cfg.ProxyRuntime.EgressMode = "direct"
	cfg.ProxyRuntime.Clearance.Enabled = true
	cfg.ProxyRuntime.Clearance.Mode = "flaresolverr"
	cfg.ProxyRuntime.Clearance.FlareSolverrURL = solver.URL
	client := NewReloadableClient(cfg, WithHTTPClient(app.Client()))

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := client.snapshot().bootstrapWithResources(context.Background(), accounts.Account{AccessToken: "token"})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if solverHits.Load() != 1 {
		t.Fatalf("flaresolverr calls=%d", solverHits.Load())
	}
	if appHits.Load() < 2 || client.snapshot().clearanceCookie() != "cf_clearance=clear" {
		t.Fatalf("app hits=%d cookie=%q", appHits.Load(), client.snapshot().clearanceCookie())
	}
}
