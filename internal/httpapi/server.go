package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/auth"
	"imagepool/internal/browsertransport"
	"imagepool/internal/config"
	"imagepool/internal/images"
	"imagepool/internal/imagetags"
	"imagepool/internal/metrics"
	"imagepool/internal/notifications"
	"imagepool/internal/oauthlogin"
	"imagepool/internal/openaiweb"
	"imagepool/internal/persistence"
	proxyservice "imagepool/internal/proxy"
	"imagepool/internal/registration"
	"imagepool/internal/searches"
	"imagepool/internal/storage"
	"imagepool/internal/tasks"
	"imagepool/internal/texts"
	"imagepool/internal/updater"
)

type Server struct {
	cfgMu           sync.RWMutex
	cfg             config.Config
	auth            *auth.Service
	accounts        *accounts.Store
	images          *images.Service
	texts           *texts.Service
	searches        *searches.Service
	storage         *storage.Service
	tags            *imagetags.Store
	static          *staticFiles
	tasks           *tasks.Manager
	metrics         *metrics.Service
	refresh         *accounts.RefreshManager
	autoRefresh     *accounts.AutoRefreshScheduler
	oauth           *oauthlogin.Service
	debugClient     *openaiweb.ReloadableClient
	register        *registration.Manager
	updater         *updater.Service
	state           persistence.Store
	onConfigUpdated func(config.Config)
}

type stabilityResponse struct {
	metrics.Stability
	Runtime map[string]any `json:"runtime"`
}

const statusClientClosedRequest = 499

func NewServer(cfg config.Config, accountStore *accounts.Store, imageService *images.Service, textService *texts.Service, searchService *searches.Service, storageService *storage.Service, taskManager *tasks.Manager, configUpdated ...func(config.Config)) *Server {
	return newServer(cfg, accountStore, imageService, textService, searchService, storageService, taskManager, nil, registration.NewWorker(registration.WorkerOptions{}), configUpdated...)
}

func NewServerWithPersistence(cfg config.Config, accountStore *accounts.Store, imageService *images.Service, textService *texts.Service, searchService *searches.Service, storageService *storage.Service, taskManager *tasks.Manager, state persistence.Store, configUpdated ...func(config.Config)) *Server {
	return newServer(cfg, accountStore, imageService, textService, searchService, storageService, taskManager, state, registration.NewWorker(registration.WorkerOptions{}), configUpdated...)
}

func newServer(cfg config.Config, accountStore *accounts.Store, imageService *images.Service, textService *texts.Service, searchService *searches.Service, storageService *storage.Service, taskManager *tasks.Manager, state persistence.Store, registerWorker registration.Worker, configUpdated ...func(config.Config)) *Server {
	cfg = cfg.Normalize()
	var onConfigUpdated func(config.Config)
	if len(configUpdated) > 0 {
		onConfigUpdated = configUpdated[0]
	}
	authService := auth.NewService(cfg.APIKeys, cfg.AuthKeyFile)
	tagStore := imagetags.New(cfg.ImageTagsFile)
	metricService := metrics.NewService(cfg.CallLogFile)
	registerManager := registration.NewManager(cfg.RegisterFile, accountStore, registerWorker)
	if state != nil {
		authService = auth.NewServiceWithPersistence(cfg.APIKeys, state)
		tagStore = imagetags.NewWithPersistence(state)
		metricService = metrics.NewServiceWithPersistence(state)
		registerManager = registration.NewManagerWithPersistence(state, accountStore, registerWorker)
	}
	refreshManager := accounts.NewRefreshManager(accountStore, imageService, cfg.RefreshAccountConcurrency)
	return &Server{cfg: cfg, auth: authService, accounts: accountStore, images: imageService, texts: textService, searches: searchService, storage: storageService, tags: tagStore, static: newStaticFiles(cfg.WebDistDir), tasks: taskManager, metrics: metricService, refresh: refreshManager, autoRefresh: accounts.NewAutoRefreshScheduler(accountStore, refreshManager, cfg.RefreshAccountIntervalMinutes), oauth: oauthlogin.New(), debugClient: openaiweb.NewReloadableClient(cfg), register: registerManager, updater: updater.NewFromEnvironment(), state: state, onConfigUpdated: onConfigUpdated}
}

func (s *Server) StartBackground(ctx context.Context) {
	if s == nil {
		return
	}
	s.autoRefresh.Start(ctx)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	tracked := isTrackedPath(r.URL.Path)
	var recorder *responseRecorder
	if tracked {
		recorder = newResponseRecorder(w)
		w = recorder
		r = r.WithContext(metrics.WithCallMeta(r.Context(), metrics.NewCallMeta(r.URL.Path)))
		defer func() {
			endpoint, model := metrics.MetaValues(r.Context())
			errorMessage := recorder.ErrorMessage()
			s.metrics.Record(metrics.Call{Time: startedAt, Endpoint: endpoint, Model: model, Status: metricCallStatus(recorder.status, errorMessage), StatusCode: recorder.status, DurationMS: time.Since(startedAt).Milliseconds(), Error: errorMessage})
		}()
	}
	if r.URL.Path == "/health" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "app": s.currentConfig().AppName})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/health/stability" {
		w.Header().Set("Cache-Control", "no-store")
		summary := s.metrics.Summary(time.Hour)
		runtime, _ := summary["runtime"].(map[string]any)
		writeJSON(w, http.StatusOK, stabilityResponse{Stability: s.metrics.Stability(time.Minute), Runtime: runtime})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/version" {
		writeJSON(w, http.StatusOK, map[string]any{"version": "go-image-pool"})
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/images/") {
		s.handleImageFile(w, r)
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/image-thumbnails/") {
		s.handleImageThumbnail(w, r)
		return
	}
	if s.static.Serve(w, r) {
		return
	}
	// EventSource cannot attach Authorization headers, so only this admin-only SSE
	// endpoint accepts the same token via its query string.
	if r.Method == http.MethodGet && r.URL.Path == "/api/register/events" && r.Header.Get("Authorization") == "" {
		if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	}
	identity, ok := s.auth.AuthenticateRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "unauthorized", "type": "authentication_error"}})
		return
	}
	r = r.WithContext(auth.WithIdentity(r.Context(), identity))
	if r.Method == http.MethodPost && r.URL.Path == "/auth/login" {
		s.handleLogin(w, r)
		return
	}
	if !identity.IsAdmin() && !userAccessiblePath(r.URL.Path) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"message": "administrator access required", "type": "permission_error"}})
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		s.handleModels(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/images/generations":
		s.handleImageGeneration(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/images/edits":
		s.handleImageEdit(w, r, false)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		s.handleChatCompletions(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
		s.handleResponses(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
		s.handleAnthropicMessages(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/search":
		s.handleSearch(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/debug/chatgpt-web":
		s.handleDebugChatGPTWeb(w, r)
	case r.URL.Path == "/api/auth/users":
		s.handleUserKeys(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/auth/users/"):
		s.handleUserKeyItem(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/dashboard":
		s.handleDashboard(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs":
		s.handleLogs(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/logs/delete":
		s.handleLogsDelete(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/images":
		s.handleImagesList(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/images/tags":
		writeJSON(w, http.StatusOK, map[string]any{"tags": s.tags.All()})
	case r.Method == http.MethodPost && r.URL.Path == "/api/images/tags":
		s.handleImageTagsSet(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/images/tags/"):
		s.handleImageTagDelete(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/images/storage":
		writeJSON(w, http.StatusOK, s.storage.Stats())
	case r.Method == http.MethodPost && r.URL.Path == "/api/images/storage/compress":
		s.handleImagesCompress(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/images/storage/cleanup-to-target":
		s.handleImagesCleanup(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/images/delete":
		s.handleImagesDelete(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/images/download":
		s.handleImagesDownload(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/images/download/"):
		s.handleImageDownload(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/storage/info":
		writeJSON(w, http.StatusOK, map[string]any{"backend": s.storage.Stats(), "health": map[string]any{"ok": true}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/proxy/test":
		s.handleProxyTest(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/proxy/runtime":
		s.handleProxyRuntime(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/proxy/runtime":
		s.handleProxyRuntime(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/proxy/clearance/test":
		s.handleProxyClearanceTest(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/notifications/bark/test":
		s.handleBarkTest(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/register/events":
		s.handleRegisterEvents(w, r)
	case r.URL.Path == "/api/register":
		s.handleRegister(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/register/start":
		writeJSON(w, http.StatusOK, map[string]any{"register": s.register.Start()})
	case r.Method == http.MethodPost && r.URL.Path == "/api/register/stop":
		writeJSON(w, http.StatusOK, map[string]any{"register": s.register.Stop()})
	case r.Method == http.MethodPost && r.URL.Path == "/api/register/reset":
		writeJSON(w, http.StatusOK, map[string]any{"register": s.register.Reset()})
	case r.Method == http.MethodPost && r.URL.Path == "/api/register/outlook-pool/reset":
		s.handleRegisterOutlookReset(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/image-tasks":
		s.handleTaskList(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/image-tasks/generations":
		s.handleTaskGeneration(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/image-tasks/edits":
		s.handleTaskEdit(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/image-tasks/"):
		s.handleTaskItem(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/accounts/recovery-logs":
		s.handleAccountRecoveryLogs(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/accounts":
		items := s.accounts.PublicList()
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "accounts": items})
	case r.Method == http.MethodGet && r.URL.Path == "/api/accounts/summary":
		summary := s.accounts.Summary()
		active, _ := summary["active"].(int)
		writeJSON(w, http.StatusOK, map[string]any{"summary": summary, "valid_account_count": active, "healthy": active > 0, "status": "ok"})
	case r.Method == http.MethodPost && r.URL.Path == "/api/accounts/refresh":
		s.handleAccountRefresh(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/accounts/refresh/progress/"):
		s.handleAccountRefreshProgress(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/accounts/re-login":
		s.handleAccountRefresh(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/accounts/re-login/progress/"):
		s.handleAccountRefreshProgress(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/accounts/export":
		s.handleAccountExport(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/accounts/test-image":
		s.handleAccountImageTest(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/accounts/oauth/start":
		s.handleOAuthStart(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/accounts/oauth/finish":
		s.handleOAuthFinish(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/accounts":
		s.handleAccountImport(w, r)
	case r.Method == http.MethodDelete && r.URL.Path == "/api/accounts":
		s.handleAccountsDelete(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/accounts/update":
		s.handleAccountUpdate(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/external/accounts/summary":
		s.handleExternalAccountsSummary(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/external/accounts/import":
		s.handleExternalAccountsImport(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/settings":
		writeJSON(w, http.StatusOK, map[string]any{"config": s.currentConfig()})
	case r.Method == http.MethodPost && r.URL.Path == "/api/settings":
		s.handleSettingsUpdate(w, r)
	case r.URL.Path == "/api/system/update":
		s.handleSystemUpdate(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "not found"}})
	}
}

func isTrackedPath(path string) bool {
	return (strings.HasPrefix(path, "/v1/") && path != "/v1/models") || path == "/api/image-tasks/generations" || path == "/api/image-tasks/edits" || path == "/api/accounts/test-image"
}

func (s *Server) currentConfig() config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Server) setConfig(cfg config.Config) {
	s.cfgMu.Lock()
	s.cfg = cfg
	s.cfgMu.Unlock()
}

func userAccessiblePath(path string) bool {
	return path == "/auth/login" || strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/api/image-tasks")
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": "go-image-pool", "role": identity.Role, "subject_id": identity.ID, "name": identity.Name})
}

func normalizedImageCount(n int) int {
	if n <= 0 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

func (s *Server) consumeQuota(r *http.Request, endpoint, model string, requestUnits, imageUnits int) error {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		return &auth.QuotaError{Message: "missing request identity", StatusCode: http.StatusUnauthorized}
	}
	return s.auth.Consume(identity, endpoint, model, requestUnits, imageUnits)
}

func (s *Server) handleUserKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"items": s.auth.ListUserKeys()})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]any{"message": "method not allowed"}})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	item, rawKey, err := s.auth.CreateUserKey(body.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item, "key": rawKey, "items": s.auth.ListUserKeys()})
}

func (s *Server) handleUserKeyItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/auth/users/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "user key not found"}})
		return
	}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Name    *string      `json:"name"`
			Enabled *bool        `json:"enabled"`
			Key     *string      `json:"key"`
			Limits  *auth.Limits `json:"limits"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if body.Name == nil && body.Enabled == nil && body.Key == nil && body.Limits == nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("no changes supplied"))
			return
		}
		item, found, err := s.auth.UpdateUserKey(id, auth.KeyUpdate{Name: body.Name, Enabled: body.Enabled, Key: body.Key, Limits: body.Limits})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "user key not found"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item, "items": s.auth.ListUserKeys()})
	case http.MethodDelete:
		removed, err := s.auth.DeleteUserKey(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !removed {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "user key not found"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": s.auth.ListUserKeys()})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]any{"message": "method not allowed"}})
	}
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.images.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{"id": model, "object": "model", "created": 0, "owned_by": "image-pool", "permission": []any{}, "root": model, "parent": nil})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleImageGeneration(w http.ResponseWriter, r *http.Request) {
	var req images.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.OutputBaseURL = baseURL(r)
	metrics.SetModel(r.Context(), req.Model)
	if err := s.consumeQuota(r, "/v1/images/generations", req.Model, 1, normalizedImageCount(req.N)); err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	if req.Stream {
		s.streamImage(w, r, req)
		return
	}
	identity, _ := auth.IdentityFromContext(r.Context())
	_, resp, err := s.tasks.RunGenerationForOwner(r.Context(), identity.ID, req)
	if err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, resp.MarshalForOpenAI())
}

func (s *Server) handleImageEdit(w http.ResponseWriter, r *http.Request, asTask bool) {
	req, clientTaskID, err := s.parseEditRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.OutputBaseURL = baseURL(r)
	metrics.SetModel(r.Context(), req.Model)
	endpoint := "/v1/images/edits"
	if asTask {
		endpoint = "/api/image-tasks/edits"
	}
	if err := s.consumeQuota(r, endpoint, req.Model, 1, normalizedImageCount(req.N)); err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	if asTask {
		identity, _ := auth.IdentityFromContext(r.Context())
		task := s.tasks.SubmitEditForOwner(identity.ID, clientTaskID, req)
		writeJSON(w, http.StatusAccepted, task)
		return
	}
	identity, _ := auth.IdentityFromContext(r.Context())
	_, resp, err := s.tasks.RunEditForOwner(r.Context(), identity.ID, req)
	if err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, resp.MarshalForOpenAI())
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var body openaiweb.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	metrics.SetModel(r.Context(), body.Model)
	if err := s.consumeQuota(r, "/v1/chat/completions", body.Model, 1, 0); err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	if body.Stream {
		s.streamChatCompletion(w, r, body)
		return
	}
	result, err := s.texts.Generate(r.Context(), body)
	if err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	model := result.Model
	if model == "" {
		model = body.Model
	}
	if model == "" {
		model = "auto"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      responseID("chatcmpl"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": result.Text},
			"finish_reason": "stop",
		}},
		"usage": roughUsage(body.Messages, result.Text),
	})
}

func (s *Server) streamChatCompletion(w http.ResponseWriter, r *http.Request, body openaiweb.ChatRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	id := responseID("chatcmpl")
	created := time.Now().Unix()
	model := body.Model
	if model == "" {
		model = "auto"
	}
	sentRole := false
	_, err := s.texts.Stream(r.Context(), body, func(delta openaiweb.ChatDelta) error {
		payload := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model}
		if !sentRole {
			sentRole = true
			payload["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": delta.Delta}, "finish_reason": nil}}
		} else {
			payload["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"content": delta.Delta}, "finish_reason": nil}}
		}
		writeSSE(w, payload)
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil {
		writeSSE(w, map[string]any{"error": err.Error()})
	} else {
		writeSSE(w, map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req := openaiweb.ChatRequest{Model: strValue(body["model"]), Messages: responseMessages(body["input"], body["instructions"])}
	metrics.SetModel(r.Context(), req.Model)
	if err := s.consumeQuota(r, "/v1/responses", req.Model, 1, 0); err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	if truthyValue(body["stream"]) {
		s.streamResponses(w, r, req)
		return
	}
	result, err := s.texts.Generate(r.Context(), req)
	if err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, responseObject(result.Model, result.Text, req.Messages))
}

func (s *Server) streamResponses(w http.ResponseWriter, r *http.Request, req openaiweb.ChatRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	id := responseID("resp")
	model := req.Model
	if model == "" {
		model = "auto"
	}
	writeSSEEvent(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "model": model}})
	_, err := s.texts.Stream(r.Context(), req, func(delta openaiweb.ChatDelta) error {
		writeSSEEvent(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "response_id": id, "output_index": 0, "content_index": 0, "delta": delta.Delta})
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil {
		writeSSEEvent(w, "error", map[string]any{"type": "error", "error": map[string]any{"message": err.Error()}})
	} else {
		writeSSEEvent(w, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "object": "response", "status": "completed", "model": model}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model    string                  `json:"model"`
		System   any                     `json:"system"`
		Messages []openaiweb.ChatMessage `json:"messages"`
		Stream   bool                    `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	metrics.SetModel(r.Context(), body.Model)
	if err := s.consumeQuota(r, "/v1/messages", body.Model, 1, 0); err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	messages := []openaiweb.ChatMessage{}
	if sys := messageContentText(body.System); strings.TrimSpace(sys) != "" {
		messages = append(messages, openaiweb.ChatMessage{Role: "system", Content: sys})
	}
	messages = append(messages, body.Messages...)
	req := openaiweb.ChatRequest{Model: body.Model, Messages: messages}
	if body.Stream {
		s.streamAnthropicMessages(w, r, req)
		return
	}
	result, err := s.texts.Generate(r.Context(), req)
	if err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            responseID("msg"),
		"type":          "message",
		"role":          "assistant",
		"model":         result.Model,
		"content":       []any{map[string]any{"type": "text", "text": result.Text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": roughTextTokens(messagesText(req.Messages)), "output_tokens": roughTextTokens(result.Text)},
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	metrics.SetModel(r.Context(), s.currentConfig().SearchModel)
	if err := s.consumeQuota(r, "/v1/search", s.currentConfig().SearchModel, 1, 0); err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	result, err := s.searches.Search(r.Context(), body.Prompt)
	if err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) streamAnthropicMessages(w http.ResponseWriter, r *http.Request, req openaiweb.ChatRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	id := responseID("msg")
	model := req.Model
	if model == "" {
		model = "auto"
	}
	writeSSEEvent(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": roughTextTokens(messagesText(req.Messages)), "output_tokens": 0}}})
	writeSSEEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	var output string
	_, err := s.texts.Stream(r.Context(), req, func(delta openaiweb.ChatDelta) error {
		output += delta.Delta
		writeSSEEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": delta.Delta}})
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil {
		writeSSEEvent(w, "error", map[string]any{"type": "error", "error": map[string]any{"message": err.Error()}})
	} else {
		writeSSEEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		writeSSEEvent(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": roughTextTokens(output)}})
		writeSSEEvent(w, "message_stop", map[string]any{"type": "message_stop"})
	}
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) streamImage(w http.ResponseWriter, r *http.Request, req images.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	req.Stream = false
	identity, _ := auth.IdentityFromContext(r.Context())
	_, resp, err := s.tasks.RunGenerationForOwner(r.Context(), identity.ID, req)
	if err != nil {
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"error": err.Error()}))
	} else {
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(resp.MarshalForOpenAI()))
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *Server) handleTaskGeneration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientTaskID string `json:"client_task_id"`
		Prompt       string `json:"prompt"`
		Model        string `json:"model"`
		Size         string `json:"size"`
		Quality      string `json:"quality"`
		N            int    `json:"n"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	metrics.SetModel(r.Context(), body.Model)
	if err := s.consumeQuota(r, "/api/image-tasks/generations", body.Model, 1, normalizedImageCount(body.N)); err != nil {
		writeError(w, statusFromError(err), err)
		return
	}
	identity, _ := auth.IdentityFromContext(r.Context())
	task := s.tasks.SubmitGenerationForOwner(identity.ID, body.ClientTaskID, images.Request{Prompt: body.Prompt, Model: body.Model, Size: body.Size, Quality: body.Quality, N: normalizedImageCount(body.N), OutputBaseURL: baseURL(r)})
	writeJSON(w, http.StatusAccepted, task)
}
func (s *Server) handleTaskEdit(w http.ResponseWriter, r *http.Request) {
	s.handleImageEdit(w, r, true)
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	ids := []string{}
	for _, part := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if part = strings.TrimSpace(part); part != "" {
			ids = append(ids, part)
		}
	}
	identity, _ := auth.IdentityFromContext(r.Context())
	items := s.tasks.ListForOwner(ids, identity.ID, identity.IsAdmin())
	compact := strings.TrimSpace(r.URL.Query().Get("compact")) == "1"
	if compact {
		items = compactTaskList(items)
	}
	found := map[string]bool{}
	for _, item := range items {
		found[item.ID] = true
	}
	missing := []string{}
	for _, id := range ids {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	response := map[string]any{"items": items, "missing_ids": missing}
	if !compact {
		// Keep the legacy alias for integrations that have not opted into the
		// compact list response yet.
		response["tasks"] = items
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleTaskItem(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/image-tasks/"), "/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}
	id, action := parts[0], parts[1]
	identity, _ := auth.IdentityFromContext(r.Context())
	if action == "status" && r.Method == http.MethodGet {
		if task, ok := s.tasks.StatusForOwner(id, identity.ID, identity.IsAdmin()); ok {
			writeJSON(w, http.StatusOK, task)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}
	if action == "cancel" && r.Method == http.MethodPost {
		if task, ok := s.tasks.CancelForOwner(id, identity.ID, identity.IsAdmin()); ok {
			writeJSON(w, http.StatusOK, task)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}
	if action == "resume-poll" && r.Method == http.MethodPost {
		var body struct {
			ExtraTimeoutSecs float64 `json:"extra_timeout_secs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ExtraTimeoutSecs <= 0 {
			body.ExtraTimeoutSecs = 30
		}
		if task, ok := s.tasks.ResumePollForOwner(id, identity.ID, identity.IsAdmin(), body.ExtraTimeoutSecs); ok {
			writeJSON(w, http.StatusOK, task)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
}

func (s *Server) handleAccountImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tokens   []string           `json:"tokens"`
		Accounts []accounts.Account `json:"accounts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items := append([]accounts.Account(nil), body.Accounts...)
	for _, token := range body.Tokens {
		if token = strings.TrimSpace(token); token != "" {
			items = append(items, accounts.Account{AccessToken: token})
		}
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("tokens or accounts is required"))
		return
	}
	added, skipped, refreshed, issues, err := s.importAccountsAndValidate(items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped, "refreshed": refreshed, "errors": issues, "items": s.accounts.PublicList()})
}

func (s *Server) importAccountsAndValidate(items []accounts.Account) (added, skipped, refreshed int, issues []map[string]string, err error) {
	validationTokens := make([]string, 0, len(items))
	seen := map[string]bool{}
	for i := range items {
		token := strings.TrimSpace(items[i].AccessToken)
		items[i].AccessToken = token
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		if _, exists := s.accounts.Get(token); exists {
			continue
		}
		// The account cannot be dispatched between persistence and validation.
		items[i].Status = "pending_validation"
		validationTokens = append(validationTokens, token)
	}
	added, skipped, err = s.accounts.AddWithResult(items)
	if err != nil || len(validationTokens) == 0 {
		return added, skipped, 0, nil, err
	}
	progress, err := s.refresh.RefreshNow(validationTokens)
	if err != nil {
		return added, skipped, 0, nil, err
	}
	issues = make([]map[string]string, 0)
	for _, result := range progress.Results {
		switch result.Status {
		case "success":
			refreshed++
		case "removed":
			issues = append(issues, map[string]string{"access_token": result.Token, "error": "token invalidated and removed"})
		default:
			message := strings.TrimSpace(result.Error)
			if message == "" {
				message = "account validation failed"
			}
			issues = append(issues, map[string]string{"access_token": result.Token, "error": message})
		}
	}
	return added, skipped, refreshed, issues, nil
}

func (s *Server) handleAccountsDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tokens []string `json:"tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(body.Tokens) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("tokens is required"))
		return
	}
	removed, err := s.accounts.Delete(body.Tokens)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "items": s.accounts.PublicList()})
}

func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessToken string  `json:"access_token"`
		Type        *string `json:"type"`
		Status      *string `json:"status"`
		Quota       *int    `json:"quota"`
		Proxy       *string `json:"proxy"`
		Email       *string `json:"email"`
		Password    *string `json:"password"`
		Disabled    *bool   `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.AccessToken) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("access_token is required"))
		return
	}
	updates := map[string]any{}
	for key, value := range map[string]any{"type": body.Type, "status": body.Status, "quota": body.Quota, "proxy": body.Proxy, "email": body.Email, "password": body.Password, "disabled": body.Disabled} {
		switch typed := value.(type) {
		case *string:
			if typed != nil {
				updates[key] = *typed
			}
		case *int:
			if typed != nil {
				updates[key] = *typed
			}
		case *bool:
			if typed != nil {
				updates[key] = *typed
			}
		}
	}
	if len(updates) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("no changes supplied"))
		return
	}
	item, found, err := s.accounts.Update(body.AccessToken, updates)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "account not found"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item.Public(), "items": s.accounts.PublicList()})
}

func (s *Server) handleAccountRecoveryLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	if limit <= 0 {
		limit = 200
	}
	items := s.accounts.CredentialRecoveryLogs(strings.TrimSpace(r.URL.Query().Get("email")), limit)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAccountRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessTokens []string `json:"access_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(body.AccessTokens) == 0 {
		body.AccessTokens = s.accounts.Tokens()
	}
	progressID, err := s.refresh.Start(body.AccessTokens)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress_id": progressID})
}

func (s *Server) handleAccountRefreshProgress(w http.ResponseWriter, r *http.Request) {
	prefix := "/api/accounts/refresh/progress/"
	if strings.HasPrefix(r.URL.Path, "/api/accounts/re-login/progress/") {
		prefix = "/api/accounts/re-login/progress/"
	}
	id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, prefix))
	progress, ok := s.refresh.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "progress not found"}})
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (s *Server) handleAccountImageTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessToken string `json:"access_token"`
		Prompt      string `json:"prompt"`
		Model       string `json:"model"`
		Size        string `json:"size"`
		Quality     string `json:"quality"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(body.AccessToken) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("access_token is required"))
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		body.Prompt = "Generate a simple small blue circle on a white background."
	}
	metrics.SetModel(r.Context(), body.Model)
	identity, _ := auth.IdentityFromContext(r.Context())
	task, response, err := s.tasks.RunGenerationWithAccountForOwner(r.Context(), identity.ID, body.AccessToken, images.Request{Prompt: body.Prompt, Model: body.Model, Size: body.Size, Quality: body.Quality, N: 1, OutputBaseURL: baseURL(r)})
	if err != nil {
		message := err.Error()
		code := "upstream_error"
		if openaiweb.IsInteractiveChallengeError(err) {
			message = "ChatGPT 当前要求 Turnstile 人机验证；这不是账号失效或额度不足，需在正常浏览器会话完成验证后再试。"
			code = "interactive_challenge_required"
		}
		writeJSON(w, statusFromError(err), map[string]any{"ok": false, "error": message, "code": code, "task_id": task.ID, "items": s.accounts.PublicList()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": response.Created, "image_count": len(response.Data), "task_id": task.ID, "items": s.accounts.PublicList()})
}

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EmailHint string `json:"email_hint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.oauth.Start(body.EmailHint)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleOAuthFinish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		Callback  string `json:"callback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tokens, err := s.oauth.Finish(body.SessionID, body.Callback)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	added, skipped, refreshed, issues, err := s.importAccountsAndValidate([]accounts.Account{{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken, SourceType: "oauth_login", Status: "正常"}})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped, "refreshed": refreshed, "errors": issues, "items": s.accounts.PublicList()})
}

func (s *Server) handleDebugChatGPTWeb(w http.ResponseWriter, r *http.Request) {
	var body openaiweb.DebugRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.debugClient.Debug(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAccountExport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccessTokens []string `json:"access_tokens"`
		Format       string   `json:"format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items := s.accounts.Export(body.AccessTokens)
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("no complete accounts available for export"))
		return
	}
	timestamp := time.Now().Format("20060102-150405")
	if strings.EqualFold(body.Format, "zip") {
		buffer := new(bytes.Buffer)
		archive := zip.NewWriter(buffer)
		for index, item := range items {
			writer, err := archive.Create(exportFileName(item["access_token"], index) + ".json")
			if err != nil {
				continue
			}
			data, _ := json.MarshalIndent(item, "", "  ")
			_, _ = writer.Write(append(data, '\n'))
		}
		if err := archive.Close(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="image-pool-accounts-%s.zip"`, timestamp))
		_, _ = w.Write(buffer.Bytes())
		return
	}
	payload := any(items)
	if len(items) == 1 {
		payload = items[0]
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="image-pool-accounts-%s.json"`, timestamp))
	_, _ = w.Write(append(data, '\n'))
}

func exportFileName(token string, index int) string {
	token = strings.TrimSpace(token)
	if len(token) > 12 {
		token = token[:12]
	}
	if token == "" {
		return fmt.Sprintf("account-%03d", index+1)
	}
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, token)
}

func (s *Server) handleExternalAccountsSummary(w http.ResponseWriter, r *http.Request) {
	summary := s.accounts.Summary()
	active, _ := summary["active"].(int)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "valid_account_count": active, "healthy": active > 0, "status": "ok", "summary": summary})
}

func (s *Server) handleExternalAccountsImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tokens   []string           `json:"tokens"`
		Accounts []accounts.Account `json:"accounts"`
		Refresh  bool               `json:"refresh"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items := append([]accounts.Account(nil), body.Accounts...)
	for _, token := range body.Tokens {
		if token = strings.TrimSpace(token); token != "" {
			items = append(items, accounts.Account{AccessToken: token, SourceType: "external_api"})
		}
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("tokens or accounts is required"))
		return
	}
	added, skipped, refreshed, issues, err := s.importAccountsAndValidate(items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	summary := s.accounts.Summary()
	active, _ := summary["active"].(int)
	response := map[string]any{"ok": true, "added": added, "skipped": skipped, "refreshed": refreshed, "errors": issues, "valid_account_count": active, "healthy": active > 0, "status": "ok", "items": s.accounts.PublicList()}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentConfig()
	var storageStats map[string]any
	var storageHealth map[string]any
	if s.state != nil {
		health, err := s.state.Health(r.Context())
		storageStats = map[string]any{"type": "database", "db_type": health.Backend, "description": health.Description, "database_url": health.DatabaseURL}
		storageHealth = map[string]any{"status": "healthy", "backend": health.Backend}
		if err != nil {
			storageHealth["status"] = "unhealthy"
			storageHealth["error"] = err.Error()
		}
	} else {
		storageStats = s.storage.Stats()
		storageStats["type"] = "local"
		storageStats["description"] = "本地文件存储"
		storageHealth = map[string]any{"status": "healthy", "backend": "local"}
	}
	taskList := s.tasks.List(nil)
	counts := map[string]int{}
	for _, task := range taskList {
		counts[task.Status]++
	}
	accountSummary := s.accounts.Summary()
	userKeys := s.auth.ListUserKeys()
	enabledUsers := 0
	for _, item := range userKeys {
		if item.Enabled {
			enabledUsers++
		}
	}
	runtimeWindow := dashboardRuntimeWindow(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"app":          cfg.AppName,
		"version":      "go-image-pool",
		"generated_at": time.Now(),
		"storage":      map[string]any{"backend": storageStats, "health": mergeDashboardStorageHealth(storageHealth, accountSummary["total"], len(userKeys))},
		"accounts":     accountSummary,
		"auth_keys":    map[string]any{"users": len(userKeys), "enabled_users": enabledUsers},
		"calls":        s.metrics.Summary(runtimeWindow),
		"tasks": map[string]any{
			"total":     len(taskList),
			"by_status": counts,
			"recent":    firstTasks(taskList, 10),
		},
		"models": cfg.Models,
	})
}

func dashboardRuntimeWindow(r *http.Request) time.Duration {
	minutes, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("runtime_window_minutes")))
	if err != nil {
		return time.Hour
	}
	switch minutes {
	case 60, 24 * 60, 7 * 24 * 60, 30 * 24 * 60:
		return time.Duration(minutes) * time.Minute
	default:
		return time.Hour
	}
}

func mergeDashboardStorageHealth(health map[string]any, accountsTotal any, authKeyCount int) map[string]any {
	result := map[string]any{}
	for key, value := range health {
		result[key] = value
	}
	result["account_count"] = accountsTotal
	result["auth_key_count"] = authKeyCount
	return result
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	logType := strings.TrimSpace(r.URL.Query().Get("type"))
	if logType == "account" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "logs": []any{}})
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if logType != "" && logType != "call" {
		status = logType
	}
	calls := s.metrics.List(status, strings.TrimSpace(r.URL.Query().Get("start_date")), strings.TrimSpace(r.URL.Query().Get("end_date")))
	items := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		items = append(items, callLogItem(call))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "logs": items})
}

func callLogItem(call metrics.Call) map[string]any {
	resultLabel := "成功"
	if call.StatusCode >= http.StatusBadRequest || call.Status == "failed" || call.Status == "error" {
		resultLabel = "失败"
	}
	return map[string]any{
		"id":      call.ID,
		"time":    call.Time,
		"type":    "call",
		"summary": call.Summary,
		"detail": map[string]any{
			"status":             call.Status,
			"status_code":        call.StatusCode,
			"duration_ms":        call.DurationMS,
			"endpoint":           call.Endpoint,
			"model":              call.Model,
			"error":              call.Error,
			"final_result_label": resultLabel,
		},
	}
}

func (s *Server) handleLogsDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, map[string]any{"removed": s.metrics.Delete(body.IDs)})
}

func (s *Server) handleImagesList(w http.ResponseWriter, r *http.Request) {
	items, err := s.storage.List(baseURL(r), strings.TrimSpace(r.URL.Query().Get("start_date")), strings.TrimSpace(r.URL.Query().Get("end_date")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for i := range items {
		items[i].Tags = s.tags.Get(items[i].Rel)
		items[i].ThumbnailURL = strings.Replace(items[i].URL, "/images/", "/image-thumbnails/", 1)
	}
	groupsByDate := map[string][]storage.ImageItem{}
	for _, item := range items {
		groupsByDate[item.Date] = append(groupsByDate[item.Date], item)
	}
	dates := make([]string, 0, len(groupsByDate))
	for date := range groupsByDate {
		dates = append(dates, date)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	groups := make([]map[string]any, 0, len(dates))
	for _, date := range dates {
		groups = append(groups, map[string]any{"date": date, "items": groupsByDate[date]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "groups": groups})
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if value := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); value != "" {
		scheme = value
	}

	host := r.Host
	if value := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); value != "" {
		host = value
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func firstForwardedValue(value string) string {
	value = strings.TrimSpace(strings.Split(value, ",")[0])
	return strings.TrimSpace(value)
}

func (s *Server) handleImageFile(w http.ResponseWriter, r *http.Request) {
	rel, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/images/"))
	f, name, err := s.storage.Open(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) handleImageThumbnail(w http.ResponseWriter, r *http.Request) {
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/images/" + strings.TrimPrefix(r.URL.Path, "/image-thumbnails/")
	s.handleImageFile(w, r2)
}

func (s *Server) handleImageDownload(w http.ResponseWriter, r *http.Request) {
	rel, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/images/download/"))
	f, name, err := s.storage.Open(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	info, _ := f.Stat()
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) handleImagesDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	removed, err := s.storage.Delete(body.Paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.tags.RemovePaths(body.Paths)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": removed, "removed": removed})
}

func (s *Server) handleImagesCompress(w http.ResponseWriter, r *http.Request) {
	compressed, savedBytes, err := s.storage.Compress()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"compressed": compressed, "saved_bytes": savedBytes, "saved_mb": float64(savedBytes) / (1024 * 1024)})
}

func (s *Server) handleImagesCleanup(w http.ResponseWriter, r *http.Request) {
	target, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("target_free_mb")), 10, 64)
	if err != nil || target <= 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("target_free_mb must be greater than zero"))
		return
	}
	removed, freedBytes, paths, done, err := s.storage.CleanupToFreeMB(target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(paths) > 0 {
		_ = s.tags.RemovePaths(paths)
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "freed_bytes": freedBytes, "freed_mb": float64(freedBytes) / (1024 * 1024), "done": done})
}

func (s *Server) handleImageTagsSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string   `json:"path"`
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	f, _, err := s.storage.Open(body.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("image not found"))
		return
	}
	f.Close()
	tags, err := s.tags.Set(body.Path, body.Tags)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": tags})
}

func (s *Server) handleImageTagDelete(w http.ResponseWriter, r *http.Request) {
	tag, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/images/tags/"))
	removed, err := s.tags.DeleteTag(tag)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed_from": removed})
}

func (s *Server) handleImagesDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	reader, err := s.storage.Zip(body.Paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="images.zip"`)
	_, _ = io.Copy(w, reader)
}

func (s *Server) handleProxyTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg := s.currentConfig()
	runtime := cfg.ProxyRuntime
	if value := strings.TrimSpace(body.URL); value != "" {
		runtime.Enabled = true
		runtime.EgressMode = "single_proxy"
		runtime.ProxyURL = proxyservice.NormalizeURL(value)
	}
	if proxyservice.EffectiveURL(runtime, false) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": false, "status": 0, "latency_ms": 0, "error": "no active proxy configured", "runtime": proxyservice.RuntimeStatus(runtime)}})
		return
	}
	if err := proxyservice.ValidateURL(proxyservice.EffectiveURL(runtime, false)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	timeout := time.Duration(cfg.RequestTimeoutSecs * float64(time.Second))
	if cfg.UpstreamTransport == "tls_client" {
		client, err := browsertransport.NewHTTPClient(runtime, timeout, false)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"result": proxyservice.Test(r.Context(), runtime, "", timeout)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": proxyservice.TestWithClient(r.Context(), client, runtime, "")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": proxyservice.Test(r.Context(), runtime, "", timeout)})
}

func (s *Server) handleProxyRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg := s.currentConfig()
		writeJSON(w, http.StatusOK, map[string]any{"runtime": proxyservice.PublicRuntime(cfg.ProxyRuntime), "status": proxyservice.RuntimeStatus(cfg.ProxyRuntime)})
		return
	}
	var runtime config.ProxyRuntime
	if err := json.NewDecoder(r.Body).Decode(&runtime); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runtime.ProxyURL = proxyservice.NormalizeURL(runtime.ProxyURL)
	runtime.ResourceProxyURL = proxyservice.NormalizeURL(runtime.ResourceProxyURL)
	for _, value := range []string{runtime.ProxyURL, runtime.ResourceProxyURL} {
		if err := proxyservice.ValidateURL(value); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	next, err := s.applyConfigPatch(map[string]any{"proxy_runtime": runtime})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime": proxyservice.PublicRuntime(next.ProxyRuntime), "status": proxyservice.RuntimeStatus(next.ProxyRuntime)})
}

func (s *Server) handleProxyClearanceTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TargetURL string `json:"target_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg := s.currentConfig()
	runtime := cfg.ProxyRuntime
	clearance := runtime.Clearance
	if !runtime.Enabled || !clearance.Enabled || clearance.Mode == "none" {
		writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": false, "status": "disabled", "latency_ms": 0, "has_cookies": false, "user_agent": "", "error": "clearance is disabled", "runtime": proxyservice.RuntimeStatus(runtime)}})
		return
	}
	target := strings.TrimSpace(body.TargetURL)
	if target == "" {
		target = "https://chatgpt.com"
	}
	if clearance.Mode == "flaresolverr" {
		solution, err := proxyservice.SolveFlareSolverr(r.Context(), runtime, target)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": false, "status": "failed", "latency_ms": 0, "has_cookies": false, "user_agent": "", "error": err.Error(), "runtime": proxyservice.RuntimeStatus(runtime)}})
			return
		}
		runtime.Clearance.CFCookies = solution.Cookies
		runtime.Clearance.CFClearance = solution.Clearance
		if solution.UserAgent != "" {
			runtime.Clearance.UserAgent = solution.UserAgent
		}
		updated, err := s.applyConfigPatch(map[string]any{"proxy_runtime": runtime})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cfg, runtime, clearance = updated, updated.ProxyRuntime, updated.ProxyRuntime.Clearance
	}
	result := proxyservice.Test(r.Context(), runtime, target, time.Duration(cfg.RequestTimeoutSecs*float64(time.Second)))
	result["has_cookies"] = clearance.CFCookies != "" || clearance.CFClearance != ""
	result["user_agent"] = clearance.UserAgent
	if ok, _ := result["ok"].(bool); ok {
		result["status"] = "ok"
	} else {
		result["status"] = "failed"
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (s *Server) handleBarkTest(w http.ResponseWriter, r *http.Request) {
	result := notifications.TestBark(r.Context(), s.currentConfig().Notifications.Bark, nil)
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"register": s.register.Get()})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]any{"message": "method not allowed"}})
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": s.register.Update(patch)})
}

func (s *Server) handleRegisterEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	emit := func() bool {
		payload, err := json.Marshal(s.register.Get())
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return err == nil
	}
	if !emit() {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !emit() {
				return
			}
		}
	}
}

func (s *Server) handleRegisterOutlookReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope string `json:"scope"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, map[string]any{"register": s.register.ResetOutlookPool(strings.TrimSpace(body.Scope))})
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	next, err := s.applyConfigPatch(patch)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": next})
}

func (s *Server) applyConfigPatch(patch map[string]any) (config.Config, error) {
	if err := validateAdminKeyPatch(patch); err != nil {
		return config.Config{}, err
	}
	current := s.currentConfig()
	raw, err := json.Marshal(current)
	if err != nil {
		return config.Config{}, err
	}
	var merged map[string]any
	if err := json.Unmarshal(raw, &merged); err != nil {
		return config.Config{}, err
	}
	for key, value := range patch {
		merged[key] = value
	}
	nextRaw, err := json.Marshal(merged)
	if err != nil {
		return config.Config{}, err
	}
	next := current
	if err := json.Unmarshal(nextRaw, &next); err != nil {
		return config.Config{}, err
	}
	next = next.Normalize()
	if err := next.Save(); err != nil {
		return config.Config{}, err
	}
	s.auth.UpdateAdminKeys(next.APIKeys)
	s.setConfig(next)
	s.refresh.SetConcurrency(next.RefreshAccountConcurrency)
	s.autoRefresh.UpdateInterval(next.RefreshAccountIntervalMinutes)
	s.debugClient.UpdateConfig(next)
	if s.onConfigUpdated != nil {
		s.onConfigUpdated(next)
	}
	return next, nil
}

func validateAdminKeyPatch(patch map[string]any) error {
	rawKeys, ok := patch["api_keys"]
	if !ok {
		return nil
	}

	var keys []string
	switch value := rawKeys.(type) {
	case []any:
		keys = make([]string, 0, len(value))
		for _, item := range value {
			key, ok := item.(string)
			if !ok {
				return errors.New("api_keys must contain strings")
			}
			keys = append(keys, key)
		}
	case []string:
		keys = value
	default:
		return errors.New("api_keys must be an array")
	}

	for _, key := range keys {
		if strings.TrimSpace(key) != "" {
			return nil
		}
	}
	return errors.New("at least one administrator API key is required")
}

func (s *Server) parseEditRequest(r *http.Request) (images.Request, string, error) {
	if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return images.Request{}, "", err
		}
		form := r.MultipartForm
		req := images.Request{Prompt: formValue(form, "prompt"), Model: formValue(form, "model"), Size: formValue(form, "size"), Quality: formValue(form, "quality"), ResponseFormat: formValue(form, "response_format")}
		if n, _ := strconv.Atoi(formValue(form, "n")); n > 0 {
			req.N = n
		}
		for _, key := range []string{"image", "images"} {
			for _, fh := range form.File[key] {
				f, err := fh.Open()
				if err != nil {
					return images.Request{}, "", err
				}
				data, err := io.ReadAll(io.LimitReader(f, 30<<20))
				f.Close()
				if err != nil {
					return images.Request{}, "", err
				}
				img, err := openaiweb.ImageInputFromBytes(fh.Filename, fh.Header.Get("Content-Type"), data)
				if err != nil {
					return images.Request{}, "", err
				}
				req.References = append(req.References, img)
			}
		}
		return req, formValue(form, "client_task_id"), nil
	}
	var body struct {
		ClientTaskID   string   `json:"client_task_id"`
		Prompt         string   `json:"prompt"`
		Model          string   `json:"model"`
		N              int      `json:"n"`
		Size           string   `json:"size"`
		Quality        string   `json:"quality"`
		ResponseFormat string   `json:"response_format"`
		Images         []string `json:"images"`
		Image          any      `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return images.Request{}, "", err
	}
	sources := append([]string(nil), body.Images...)
	switch v := body.Image.(type) {
	case string:
		sources = append(sources, v)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				sources = append(sources, s)
			}
		}
	}
	refs := []openaiweb.ImageInput{}
	for _, source := range sources {
		img, err := openaiweb.ImageInputFromSource(context.Background(), http.DefaultClient, source)
		if err != nil {
			return images.Request{}, "", err
		}
		refs = append(refs, img)
	}
	return images.Request{Prompt: body.Prompt, Model: body.Model, N: body.N, Size: body.Size, Quality: body.Quality, ResponseFormat: body.ResponseFormat, References: refs}, body.ClientTaskID, nil
}

func formValue(form *multipart.Form, key string) string {
	if form == nil || form.Value == nil || len(form.Value[key]) == 0 {
		return ""
	}
	return form.Value[key][0]
}

func responseID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func writeSSE(w http.ResponseWriter, payload any) {
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(payload))
}

func writeSSEEvent(w http.ResponseWriter, event string, payload any) {
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	writeSSE(w, payload)
}

func strValue(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func truthyValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func responseMessages(input any, instructions any) []openaiweb.ChatMessage {
	messages := []openaiweb.ChatMessage{}
	if sys := strings.TrimSpace(messageContentText(instructions)); sys != "" {
		messages = append(messages, openaiweb.ChatMessage{Role: "system", Content: sys})
	}
	switch v := input.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			messages = append(messages, openaiweb.ChatMessage{Role: "user", Content: v})
		}
	case []any:
		pending := []any{}
		flush := func() {
			if len(pending) > 0 {
				messages = append(messages, openaiweb.ChatMessage{Role: "user", Content: append([]any(nil), pending...)})
				pending = nil
			}
		}
		for _, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if role := strings.TrimSpace(strValue(obj["role"])); role != "" {
				flush()
				messages = append(messages, openaiweb.ChatMessage{Role: role, Content: obj["content"]})
				continue
			}
			pending = append(pending, obj)
		}
		flush()
	case map[string]any:
		role := strings.TrimSpace(strValue(v["role"]))
		if role == "" {
			role = "user"
		}
		if content, ok := v["content"]; ok {
			messages = append(messages, openaiweb.ChatMessage{Role: role, Content: content})
		} else {
			messages = append(messages, openaiweb.ChatMessage{Role: role, Content: []any{v}})
		}
	}
	if len(messages) == 0 {
		messages = append(messages, openaiweb.ChatMessage{Role: "user", Content: ""})
	}
	return messages
}

func messageContentText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			switch x := item.(type) {
			case string:
				b.WriteString(x)
			case map[string]any:
				if text := strValue(x["text"]); text != "" {
					b.WriteString(text)
				} else if text := strValue(x["content"]); text != "" {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	case map[string]any:
		if text := strValue(v["text"]); text != "" {
			return text
		}
		return messageContentText(v["content"])
	default:
		return fmt.Sprint(v)
	}
}

func messagesText(messages []openaiweb.ChatMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(messageContentText(msg.Content))
		b.WriteByte('\n')
	}
	return b.String()
}

func roughTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	tokens := len([]rune(text)) / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

func roughUsage(messages []openaiweb.ChatMessage, output string) map[string]any {
	in := roughTextTokens(messagesText(messages))
	out := roughTextTokens(output)
	return map[string]any{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out}
}

func responseObject(model, text string, messages []openaiweb.ChatMessage) map[string]any {
	if model == "" {
		model = "auto"
	}
	inputTokens := roughTextTokens(messagesText(messages))
	outputTokens := roughTextTokens(text)
	return map[string]any{
		"id":          responseID("resp"),
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"status":      "completed",
		"model":       model,
		"output_text": text,
		"output": []any{map[string]any{
			"id":      responseID("msg"),
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		}},
		"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": inputTokens + outputTokens},
	}
}

func firstTasks(items []tasks.Task, limit int) []tasks.Task {
	if limit <= 0 || len(items) == 0 {
		return nil
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return compactTaskList(items)
}

// compactTaskList keeps list and dashboard responses small. Full logs remain
// available from the per-task status endpoint when the operator opens a task.
func compactTaskList(items []tasks.Task) []tasks.Task {
	if len(items) == 0 {
		return items
	}
	out := append([]tasks.Task(nil), items...)
	for i := range out {
		out[i].StatusLogs = nil
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
func writeError(w http.ResponseWriter, status int, err error) {
	errType, code := "server_error", "upstream_error"
	switch {
	case errors.Is(err, context.Canceled):
		errType, code = "request_canceled", "request_canceled"
	case errors.Is(err, openaiweb.ErrContentPolicy):
		errType, code = "invalid_request_error", "content_policy_violation"
	case errors.Is(err, context.DeadlineExceeded):
		errType, code = "timeout_error", "upstream_timeout"
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": err.Error(), "type": errType, "code": code}})
}
func statusFromError(err error) int {
	if errors.Is(err, context.Canceled) {
		return statusClientClosedRequest
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	if errors.Is(err, openaiweb.ErrContentPolicy) {
		return http.StatusBadRequest
	}
	var quota *auth.QuotaError
	if errors.As(err, &quota) {
		return quota.StatusCode
	}
	if errors.Is(err, accounts.ErrNoAvailableAccount) || openaiweb.IsNoFreeImageQuotaError(err) {
		return http.StatusTooManyRequests
	}
	if openaiweb.IsAuthenticationError(err) {
		return http.StatusUnauthorized
	}
	if openaiweb.IsInteractiveChallengeError(err) {
		return http.StatusPreconditionRequired
	}
	return http.StatusBadGateway
}

func metricCallStatus(statusCode int, errorMessage string) string {
	if statusCode == statusClientClosedRequest || strings.Contains(strings.ToLower(errorMessage), "context canceled") {
		return "canceled"
	}
	if statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError {
		return "rejected"
	}
	if statusCode >= http.StatusInternalServerError {
		return "failed"
	}
	return "success"
}
func mustJSON(v any) string { data, _ := json.Marshal(v); return string(data) }
