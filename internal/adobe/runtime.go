package adobe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"imagepool/internal/config"
	"imagepool/internal/persistence"
	proxyservice "imagepool/internal/proxy"
)

type Runtime struct {
	config            config.AdobeConfig
	repository        *Repository
	httpClientFactory func(context.Context, string, time.Duration) (*http.Client, error)
	testImageJobs     *testImageJobManager
	tokenRefreshJobs  *tokenRefreshJobManager
	closeOnce         sync.Once
	stop              chan struct{}
	done              chan struct{}
}

func NewRuntime(postgres *persistence.Postgres, cfg config.AdobeConfig) (*Runtime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if postgres == nil || postgres.DB() == nil {
		return nil, errors.New("Adobe requires the PostgreSQL storage backend")
	}
	cipher, err := NewCipher(os.Getenv("IMAGE_POOL_MASTER_KEY"))
	if err != nil {
		return nil, err
	}
	repository, err := NewRepository(postgres.DB(), cipher)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		config: cfg, repository: repository, testImageJobs: newTestImageJobManager(),
		tokenRefreshJobs: newTokenRefreshJobManager(), stop: make(chan struct{}), done: make(chan struct{}),
	}, nil
}

func (r *Runtime) Start(context.Context) error {
	if r != nil {
		go r.maintenanceLoop()
	}
	return nil
}

func (r *Runtime) Close(context.Context) error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		close(r.stop)
		<-r.done
	})
	return nil
}

func (r *Runtime) Repository() *Repository {
	if r == nil {
		return nil
	}
	return r.repository
}

func (r *Runtime) maintenanceLoop() {
	defer close(r.done)
	cleanupTicker := time.NewTicker(30 * time.Second)
	routeTicker := time.NewTicker(time.Duration(r.config.RouteHealthIntervalSecs) * time.Second)
	tokenTicker := time.NewTicker(time.Minute)
	defer cleanupTicker.Stop()
	defer routeTicker.Stop()
	defer tokenTicker.Stop()
	go r.checkAllRoutes()
	for {
		select {
		case <-r.stop:
			return
		case <-cleanupTicker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = r.CleanupIdempotency(ctx)
			cancel()
		case <-routeTicker.C:
			go r.checkAllRoutes()
		case <-tokenTicker.C:
			go r.refreshDueCompatibleAccounts()
		}
	}
}

func (r *Runtime) refreshDueCompatibleAccounts() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	interval := time.Duration(r.config.TokenRefreshIntervalHours) * time.Hour
	accountIDs, err := r.repository.AccountsDueForCompatibleRefresh(ctx, interval)
	if err != nil {
		return
	}
	for _, accountID := range accountIDs {
		refreshCtx, refreshCancel := context.WithTimeout(ctx, 45*time.Second)
		_, _ = r.RefreshAccountToken(refreshCtx, accountID)
		refreshCancel()
	}
}

func (r *Runtime) TestRoute(ctx context.Context, routeID string) (Route, error) {
	routeURL, err := r.repository.RouteProxyURL(ctx, routeID)
	if err != nil {
		return Route{}, err
	}
	client, err := proxyservice.NewHTTPClientForRuntime(adobeRouteProxyRuntime(routeURL), 20*time.Second)
	if err == nil {
		var request *http.Request
		request, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://new.express.adobe.com/", nil)
		if err == nil {
			request.Header.Set("User-Agent", "Mozilla/5.0 (IMAGE POOL Adobe route health)")
			var response *http.Response
			response, err = client.Do(request)
			if err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusProxyAuthRequired || response.StatusCode >= 500 {
					err = fmt.Errorf("Adobe route health returned HTTP %d", response.StatusCode)
				}
			}
		}
	}
	if recordErr := r.repository.RecordRouteHealth(ctx, routeID, err, r.config.RouteFailureThreshold, r.config.RouteCooldownSecs); recordErr != nil {
		return Route{}, recordErr
	}
	route, getErr := r.repository.GetRoute(ctx, routeID)
	if getErr != nil {
		return Route{}, getErr
	}
	if route.HealthStatus == "unhealthy" {
		_, _ = r.repository.ReassignAccountsFromRoute(ctx, routeID)
	}
	return route, err
}

func (r *Runtime) checkAllRoutes() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	routes, err := r.repository.ListRoutes(ctx)
	if err != nil {
		return
	}
	for _, route := range routes {
		if route.Enabled && strings.TrimSpace(route.ID) != "" {
			_, _ = r.TestRoute(ctx, route.ID)
		}
	}
}
