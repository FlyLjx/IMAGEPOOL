package openaiweb

import (
	"context"
	"fmt"
	"sync"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	proxyservice "imagepool/internal/proxy"
)

const clearanceRefreshCooldown = 30 * time.Second

type clearanceRefreshFlight struct {
	done   chan struct{}
	client *Client
	err    error
}

// ReloadableClient swaps the underlying HTTP client atomically after a
// settings change. In-flight requests retain their original client snapshot.
type ReloadableClient struct {
	mu     sync.RWMutex
	cfg    config.Config
	client *Client
	opts   []ClientOption

	clearanceMu          sync.Mutex
	clearanceFlight      *clearanceRefreshFlight
	lastClearanceAttempt time.Time
}

func NewReloadableClient(cfg config.Config, opts ...ClientOption) *ReloadableClient {
	r := &ReloadableClient{cfg: cfg.Normalize(), opts: append([]ClientOption(nil), opts...)}
	r.client = r.newClient(r.cfg)
	return r
}

func (r *ReloadableClient) UpdateConfig(cfg config.Config) {
	if r == nil {
		return
	}
	nextCfg := cfg.Normalize()
	next := r.newClient(nextCfg)
	r.mu.Lock()
	r.cfg = nextCfg
	r.client = next
	r.mu.Unlock()
}

func (r *ReloadableClient) snapshot() *Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client
}

func (r *ReloadableClient) newClient(cfg config.Config) *Client {
	opts := append([]ClientOption(nil), r.opts...)
	if flaresolverrEnabled(cfg.ProxyRuntime) {
		opts = append(opts, withBootstrapClearanceRefresh(r.refreshBootstrapClearance))
	}
	return NewClient(cfg, opts...)
}

func flaresolverrEnabled(runtime config.ProxyRuntime) bool {
	clearance := runtime.Clearance
	return runtime.Enabled && clearance.Enabled && clearance.Mode == "flaresolverr" && clearance.FlareSolverrURL != ""
}

// refreshBootstrapClearance coalesces a burst of HTTP 403 bootstrap failures
// into one FlareSolverr request. The refreshed client is used immediately and
// is deliberately retried only once by the caller.
func (r *ReloadableClient) refreshBootstrapClearance(ctx context.Context) (*Client, error) {
	if r == nil {
		return nil, fmt.Errorf("reloadable client is unavailable")
	}
	r.clearanceMu.Lock()
	if flight := r.clearanceFlight; flight != nil {
		r.clearanceMu.Unlock()
		select {
		case <-flight.done:
			return flight.client, flight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if !r.lastClearanceAttempt.IsZero() && time.Since(r.lastClearanceAttempt) < clearanceRefreshCooldown {
		r.clearanceMu.Unlock()
		return r.snapshot(), nil
	}
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()
	if !flaresolverrEnabled(cfg.ProxyRuntime) {
		r.clearanceMu.Unlock()
		return nil, fmt.Errorf("flaresolverr clearance is not enabled")
	}
	flight := &clearanceRefreshFlight{done: make(chan struct{})}
	r.clearanceFlight = flight
	r.lastClearanceAttempt = time.Now()
	r.clearanceMu.Unlock()

	solution, err := proxyservice.SolveFlareSolverr(ctx, cfg.ProxyRuntime, cfg.ChatGPTBaseURL)
	var refreshed *Client
	if err == nil {
		r.mu.Lock()
		current := r.cfg
		if !flaresolverrEnabled(current.ProxyRuntime) || current.ProxyRuntime.Clearance.FlareSolverrURL != cfg.ProxyRuntime.Clearance.FlareSolverrURL || current.ChatGPTBaseURL != cfg.ChatGPTBaseURL {
			err = fmt.Errorf("proxy clearance settings changed while refreshing")
		} else {
			current.ProxyRuntime.Clearance.CFCookies = solution.Cookies
			current.ProxyRuntime.Clearance.CFClearance = solution.Clearance
			if solution.UserAgent != "" {
				current.ProxyRuntime.Clearance.UserAgent = solution.UserAgent
			}
			current = current.Normalize()
			r.cfg = current
			refreshed = r.newClient(current)
			r.client = refreshed
		}
		r.mu.Unlock()
	}
	if refreshed == nil {
		refreshed = r.snapshot()
	}

	r.clearanceMu.Lock()
	flight.client = refreshed
	flight.err = err
	close(flight.done)
	r.clearanceFlight = nil
	r.clearanceMu.Unlock()
	return refreshed, err
}

func (r *ReloadableClient) GenerateImage(ctx context.Context, account accounts.Account, req ImageRequest) (ImageResult, error) {
	return r.snapshot().GenerateImage(ctx, account, req)
}

func (r *ReloadableClient) ListModels(ctx context.Context, token string) ([]string, error) {
	return r.snapshot().ListModels(ctx, token)
}

func (r *ReloadableClient) ListModelsFor(ctx context.Context, account accounts.Account) ([]string, error) {
	return r.snapshot().ListModelsFor(ctx, account)
}

func (r *ReloadableClient) GetAccountInfo(ctx context.Context, token string) (AccountInfo, error) {
	return r.snapshot().GetAccountInfo(ctx, token)
}

func (r *ReloadableClient) GetAccountInfoFor(ctx context.Context, account accounts.Account) (AccountInfo, error) {
	return r.snapshot().GetAccountInfoFor(ctx, account)
}

func (r *ReloadableClient) CheckImageReady(ctx context.Context, token string) error {
	return r.snapshot().CheckImageReady(ctx, token)
}

func (r *ReloadableClient) CheckImageReadyFor(ctx context.Context, account accounts.Account) error {
	return r.snapshot().CheckImageReadyFor(ctx, account)
}

func (r *ReloadableClient) GenerateText(ctx context.Context, account accounts.Account, req ChatRequest) (ChatResult, error) {
	return r.snapshot().GenerateText(ctx, account, req)
}

func (r *ReloadableClient) StreamText(ctx context.Context, account accounts.Account, req ChatRequest, emit func(ChatDelta) error) (string, error) {
	return r.snapshot().StreamText(ctx, account, req, emit)
}

func (r *ReloadableClient) Search(ctx context.Context, account accounts.Account, req SearchRequest) (SearchResult, error) {
	return r.snapshot().Search(ctx, account, req)
}

func (r *ReloadableClient) DownloadImage(ctx context.Context, imageURL string) ([]byte, error) {
	return r.snapshot().DownloadImage(ctx, imageURL)
}

func (r *ReloadableClient) DownloadImageFor(ctx context.Context, account accounts.Account, imageURL string) ([]byte, error) {
	return r.snapshot().DownloadImageFor(ctx, account, imageURL)
}

func (r *ReloadableClient) Debug(ctx context.Context, input DebugRequest) (DebugResponse, error) {
	return r.snapshot().Debug(ctx, input)
}
