package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/httpapi"
	"imagepool/internal/images"
	"imagepool/internal/oauthlogin"
	"imagepool/internal/openaiweb"
	"imagepool/internal/persistence"
	proxyservice "imagepool/internal/proxy"
	"imagepool/internal/searches"
	"imagepool/internal/storage"
	"imagepool/internal/tasks"
	"imagepool/internal/texts"
)

func main() {
	configPath := flag.String("config", "configs/config.json", "path to config.json")
	flag.Parse()

	cfg, err := config.LoadIfExists(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	applyEnvironmentOverrides(&cfg)
	setTimezone(cfg.Timezone)
	var state persistence.Store
	if cfg.StorageBackend == "postgres" || cfg.StorageBackend == "postgresql" {
		postgres, openErr := persistence.OpenPostgres(context.Background(), cfg.DatabaseURL)
		if openErr != nil {
			log.Fatalf("connect PostgreSQL: %v", openErr)
		}
		state = postgres
		defer state.Close()
	}
	var store *accounts.Store
	if state != nil {
		store, err = accounts.LoadStoreFromPersistence(state)
	} else {
		store, err = accounts.LoadStore(cfg.AccountFile)
	}
	if err != nil {
		log.Fatalf("load accounts: %v", err)
	}
	if updated, identityErr := store.EnsureBrowserIdentities(); identityErr != nil {
		log.Fatalf("persist account browser identities: %v", identityErr)
	} else if updated > 0 {
		log.Printf("persisted browser identities for %d accounts", updated)
	}

	webClient := openaiweb.NewReloadableClient(cfg)
	storageService := storage.NewService(cfg)
	imageService := images.NewService(cfg, store, webClient, storageService)
	textService := texts.NewService(cfg, store, webClient)
	searchService := searches.NewService(cfg, store, webClient)
	tokenRefresher, refreshClientErr := newOAuthTokenRefresher(cfg)
	if refreshClientErr != nil {
		log.Printf("configure OAuth token recovery client: %v", refreshClientErr)
		tokenRefresher = oauthlogin.New()
		configureRecoveryMailbox(tokenRefresher, nil)
	}
	tokenRecovery := accounts.NewTokenRecoveryManager(cfg, store, imageService, tokenRefresher, tokenRefresher)
	tokenRecoveryCtx, cancelTokenRecovery := context.WithCancel(context.Background())
	defer cancelTokenRecovery()
	go tokenRecovery.Run(tokenRecoveryCtx)
	taskManager := tasks.NewManager(imageService)
	if state != nil {
		taskManager = tasks.NewManagerWithPersistence(imageService, state)
	}
	configUpdated := func(next config.Config) {
		setTimezone(next.Timezone)
		imageService.UpdateConfig(next)
		webClient.UpdateConfig(next)
		tokenRecovery.UpdateConfig(next)
		if nextRefresher, err := newOAuthTokenRefresher(next); err != nil {
			log.Printf("update OAuth token recovery client: %v", err)
		} else {
			tokenRecovery.UpdateRefresher(nextRefresher)
			tokenRecovery.UpdatePasswordRelogger(nextRefresher)
		}
	}
	handler := httpapi.NewServer(cfg, store, imageService, textService, searchService, storageService, taskManager, configUpdated)
	if state != nil {
		handler = httpapi.NewServerWithPersistence(cfg, store, imageService, textService, searchService, storageService, taskManager, state, configUpdated)
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("IMAGE POOL listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	cancelTokenRecovery()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func newOAuthTokenRefresher(cfg config.Config) (*oauthlogin.Service, error) {
	client, err := proxyservice.NewHTTPClientForRuntime(cfg.ProxyRuntime, time.Duration(cfg.RequestTimeoutSecs*float64(time.Second)))
	if err != nil {
		return nil, err
	}
	service := oauthlogin.NewWithClient("https://auth.openai.com", client)
	configureRecoveryMailbox(service, client)
	return service, nil
}

func configureRecoveryMailbox(service *oauthlogin.Service, client *http.Client) {
	if service == nil {
		return
	}
	apiKey := strings.TrimSpace(os.Getenv("IMAGE_POOL_YYDS_MAIL_API_KEY"))
	if apiKey == "" {
		return
	}
	apiBase := strings.TrimSpace(os.Getenv("IMAGE_POOL_YYDS_MAIL_API_BASE_URL"))
	service.SetEmailOTPReader(oauthlogin.NewYYDSMailReader(apiBase, apiKey, client))
	log.Printf("YYDS Mail has been configured for background password recovery")
}

func setTimezone(name string) {
	location, err := time.LoadLocation(name)
	if err != nil {
		log.Fatalf("load timezone %q: %v", name, err)
	}
	time.Local = location
}

func applyEnvironmentOverrides(cfg *config.Config) {
	if value := strings.TrimSpace(os.Getenv("IMAGE_POOL_LISTEN_ADDR")); value != "" {
		cfg.ListenAddr = value
	}
	overridePathFromEnv(&cfg.WebDistDir, "IMAGE_POOL_WEB_DIST_DIR")
	overridePathFromEnv(&cfg.ImageOutputDir, "IMAGE_POOL_IMAGE_OUTPUT_DIR")
}

func overridePathFromEnv(target *string, envName string) {
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		log.Fatalf("resolve %s: %v", envName, err)
	}
	*target = absolute
}
