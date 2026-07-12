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
	"imagepool/internal/openaiweb"
	"imagepool/internal/persistence"
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
	taskManager := tasks.NewManager(imageService)
	if state != nil {
		taskManager = tasks.NewManagerWithPersistence(imageService, state)
	}
	configUpdated := func(next config.Config) {
		setTimezone(next.Timezone)
		imageService.UpdateConfig(next)
		webClient.UpdateConfig(next)
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
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	defer stopBackground()
	handler.StartBackground(backgroundCtx)

	go func() {
		log.Printf("IMAGE POOL listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	stopBackground()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
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
