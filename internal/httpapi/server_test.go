package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/images"
	"imagepool/internal/metrics"
	"imagepool/internal/openaiweb"
	"imagepool/internal/registration"
	"imagepool/internal/searches"
	"imagepool/internal/storage"
	"imagepool/internal/tasks"
	"imagepool/internal/texts"
	"imagepool/internal/updater"
)

type apiBackend struct{}

type validatingAPIBackend struct {
	apiBackend
	mu              sync.Mutex
	generateErr     error
	readinessErrs   map[string]error
	readinessTokens []string
}

func (b *validatingAPIBackend) GenerateImage(ctx context.Context, account accounts.Account, req openaiweb.ImageRequest) (openaiweb.ImageResult, error) {
	b.mu.Lock()
	err := b.generateErr
	b.mu.Unlock()
	if err != nil {
		return openaiweb.ImageResult{}, err
	}
	return b.apiBackend.GenerateImage(ctx, account, req)
}

func (b *validatingAPIBackend) CheckImageReady(ctx context.Context, token string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.readinessTokens = append(b.readinessTokens, token)
	return b.readinessErrs[token]
}

func (b *validatingAPIBackend) readinessCheckCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.readinessTokens)
}

func (apiBackend) GenerateImage(ctx context.Context, account accounts.Account, req openaiweb.ImageRequest) (openaiweb.ImageResult, error) {
	return openaiweb.ImageResult{URLs: []string{"https://example.com/a.png"}, AccountEmail: account.Email, BackendModel: "gpt-5-5", ConversationID: "conv"}, nil
}
func (apiBackend) ListModels(ctx context.Context, token string) ([]string, error) {
	return []string{"gpt-5-5"}, nil
}
func (apiBackend) GenerateText(ctx context.Context, account accounts.Account, req openaiweb.ChatRequest) (openaiweb.ChatResult, error) {
	return openaiweb.ChatResult{Text: "hello", Model: req.Model, AccountEmail: account.Email, ConversationID: "conv-text"}, nil
}
func (apiBackend) StreamText(ctx context.Context, account accounts.Account, req openaiweb.ChatRequest, emit func(openaiweb.ChatDelta) error) (string, error) {
	if err := emit(openaiweb.ChatDelta{Delta: "he", ConversationID: "conv-text"}); err != nil {
		return "", err
	}
	if err := emit(openaiweb.ChatDelta{Delta: "llo", ConversationID: "conv-text"}); err != nil {
		return "", err
	}
	return "conv-text", nil
}
func (apiBackend) Search(ctx context.Context, account accounts.Account, req openaiweb.SearchRequest) (openaiweb.SearchResult, error) {
	return openaiweb.SearchResult{Answer: "search answer", Sources: []openaiweb.SearchSource{{Title: "Example", URL: "https://example.com"}}, AccountEmail: account.Email, Model: req.Model}, nil
}

func newTestServer(cfg config.Config, updated ...func(config.Config)) http.Handler {
	return newTestServerWithBackend(cfg, &validatingAPIBackend{}, updated...)
}

func newTestServerWithBackend(cfg config.Config, backend *validatingAPIBackend, updated ...func(config.Config)) http.Handler {
	cfg.APIKeys = []string{"k"}
	store := accounts.NewStore([]accounts.Account{{Email: "a", AccessToken: "tok", CreatedAt: 1}}, "")
	storageSvc := storage.NewService(cfg)
	svc := images.NewService(cfg, store, backend, storageSvc)
	textSvc := texts.NewService(cfg, store, backend)
	searchSvc := searches.NewService(cfg, store, backend)
	worker := func(_ context.Context, _ registration.Config, index int) (accounts.Account, error) {
		return accounts.Account{Email: "registered-" + strconv.Itoa(index) + "@example.test", AccessToken: "registered-" + strconv.Itoa(index), Status: "正常"}, nil
	}
	return newServer(cfg, store, svc, textSvc, searchSvc, storageSvc, tasks.NewManager(svc), nil, worker, updated...)
}

func testServer(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.AuthKeyFile = filepath.Join(t.TempDir(), "auth-keys.json")
	cfg.ImageOutputDir = filepath.Join(t.TempDir(), "images")
	cfg.CallLogFile = filepath.Join(t.TempDir(), "calls.json")
	cfg.ImageTagsFile = filepath.Join(t.TempDir(), "tags.json")
	cfg.RegisterFile = filepath.Join(t.TempDir(), "register.json")
	return newTestServer(cfg)
}

func testServerWithGenerateError(t *testing.T, generateErr error) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.AuthKeyFile = filepath.Join(t.TempDir(), "auth-keys.json")
	cfg.ImageOutputDir = filepath.Join(t.TempDir(), "images")
	cfg.CallLogFile = filepath.Join(t.TempDir(), "calls.json")
	cfg.ImageTagsFile = filepath.Join(t.TempDir(), "tags.json")
	cfg.RegisterFile = filepath.Join(t.TempDir(), "register.json")
	return newTestServerWithBackend(cfg, &validatingAPIBackend{generateErr: generateErr})
}

func TestHealthAndAuth(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health status=%v err=%v", resp.StatusCode, err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("auth status=%v err=%v", resp.StatusCode, err)
	}
}

func TestStabilityHealthEndpointIsPublicAndNoStore(t *testing.T) {
	handler := testServer(t)
	server, ok := handler.(*Server)
	if !ok {
		t.Fatalf("handler=%T", handler)
	}
	server.metrics.Record(metrics.Call{Time: time.Now(), Endpoint: "/v1/images/generations", Status: "success"})
	srv := httptest.NewServer(server)
	defer srv.Close()

	response, err := http.Get(srv.URL + "/health/stability")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var stability struct {
		metrics.Stability
		Runtime map[string]any `json:"runtime"`
	}
	if err := json.NewDecoder(response.Body).Decode(&stability); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || stability.WindowSeconds != 60 || stability.Total != 1 || stability.Status != "stable" || stability.Runtime["window_minutes"] != float64(60) {
		t.Fatalf("status=%d cache=%q stability=%#v", response.StatusCode, response.Header.Get("Cache-Control"), stability)
	}
}

func TestErrorClassificationDoesNotTreatCanceledOrRejectedRequestsAsFailures(t *testing.T) {
	if got := statusFromError(context.Canceled); got != statusClientClosedRequest {
		t.Fatalf("canceled status=%d", got)
	}
	if got := statusFromError(openaiweb.ErrContentPolicy); got != http.StatusBadRequest {
		t.Fatalf("policy status=%d", got)
	}
	if got := metricCallStatus(statusClientClosedRequest, "context canceled"); got != "canceled" {
		t.Fatalf("canceled metric status=%q", got)
	}
	if got := metricCallStatus(http.StatusBadRequest, "content policy violation"); got != "rejected" {
		t.Fatalf("rejected metric status=%q", got)
	}
}

func TestImageTimeoutSentinelsReturnGatewayTimeout(t *testing.T) {
	for name, err := range map[string]error{
		"generation":  fmt.Errorf("generation: %w", openaiweb.ErrPollTimeout),
		"preparation": fmt.Errorf("preparation: %w", openaiweb.ErrImagePreparationTimeout),
	} {
		t.Run(name, func(t *testing.T) {
			if status := statusFromError(err); status != http.StatusGatewayTimeout {
				t.Fatalf("status=%d", status)
			}
			recorder := httptest.NewRecorder()
			writeError(recorder, statusFromError(err), err)
			var body struct {
				Error struct {
					Type string `json:"type"`
					Code string `json:"code"`
				} `json:"error"`
			}
			if decodeErr := json.NewDecoder(recorder.Body).Decode(&body); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if recorder.Code != http.StatusGatewayTimeout || body.Error.Type != "timeout_error" || body.Error.Code != "upstream_timeout" {
				t.Fatalf("status=%d body=%#v", recorder.Code, body)
			}
		})
	}
}

func TestImageGenerationEndpointReturnsTimeoutError(t *testing.T) {
	srv := httptest.NewServer(testServerWithGenerateError(t, openaiweb.ErrPollTimeout))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusGatewayTimeout || body.Error.Type != "timeout_error" || body.Error.Code != "upstream_timeout" {
		t.Fatalf("status=%d body=%#v", response.StatusCode, body)
	}
}

func TestSystemUpdateEndpointTriggersInternalUpdater(t *testing.T) {
	triggered := make(chan struct{}, 1)
	updaterServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/update" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		triggered <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer updaterServer.Close()

	handler := testServer(t)
	server, ok := handler.(*Server)
	if !ok {
		t.Fatalf("handler=%T", handler)
	}
	server.updater = updater.New(updaterServer.URL+"/v1/update", "test-token")
	srv := httptest.NewServer(server)
	defer srv.Close()

	request, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/system/update", strings.NewReader(`{"version":"0.1.3"}`))
	request.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", response.StatusCode)
	}
	select {
	case <-triggered:
	case <-time.After(2 * time.Second):
		t.Fatal("updater was not triggered")
	}
}

func TestRegisterEventStreamAcceptsEventSourceToken(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/register/events?token=k", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: {") {
		t.Fatalf("line=%q err=%v", line, err)
	}
}

func TestImageGenerationEndpoint(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var payload map[string]any
	json.NewDecoder(resp.Body).Decode(&payload)
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("payload=%#v", payload)
	}
	tasksRequest, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/image-tasks", nil)
	tasksRequest.Header.Set("Authorization", "Bearer k")
	tasksResponse, err := http.DefaultClient.Do(tasksRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer tasksResponse.Body.Close()
	var taskPayload struct {
		Items []tasks.Task `json:"items"`
	}
	if err := json.NewDecoder(tasksResponse.Body).Decode(&taskPayload); err != nil {
		t.Fatal(err)
	}
	if tasksResponse.StatusCode != http.StatusOK || len(taskPayload.Items) != 1 || taskPayload.Items[0].Status != tasks.StatusSucceeded {
		t.Fatalf("task status=%d payload=%#v", tasksResponse.StatusCode, taskPayload)
	}
}

func TestCredentialFailureNeverLeaksUpstreamDiagnostics(t *testing.T) {
	raw := &openaiweb.UpstreamError{Path: "/backend-api/files", StatusCode: http.StatusUnauthorized, Body: `{"error":{"code":"token_revoked","message":"invalidated oauth token"}}`}
	assertRedacted := func(t *testing.T, body []byte) {
		t.Helper()
		text := strings.ToLower(string(body))
		for _, leaked := range []string{"/backend-api/", "token_revoked", "invalidated oauth token", "body="} {
			if strings.Contains(text, leaked) {
				t.Fatalf("response leaked %q: %s", leaked, body)
			}
		}
	}
	assertPublicMessage := func(t *testing.T, body []byte) {
		t.Helper()
		if !strings.Contains(string(body), openaiweb.PublicCredentialInvalidMessage) {
			t.Fatalf("missing public credential message: %s", body)
		}
	}

	t.Run("json", func(t *testing.T) {
		srv := httptest.NewServer(testServerWithGenerateError(t, raw))
		defer srv.Close()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw"}`))
		req.Header.Set("Authorization", "Bearer k")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		var payload struct {
			Error struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Error.Type != "authentication_error" || payload.Error.Code != "account_credential_invalid" {
			t.Fatalf("payload=%s", body)
		}
		assertRedacted(t, body)
		assertPublicMessage(t, body)
	})

	t.Run("sse", func(t *testing.T) {
		srv := httptest.NewServer(testServerWithGenerateError(t, raw))
		defer srv.Close()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw","stream":true}`))
		req.Header.Set("Authorization", "Bearer k")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		assertRedacted(t, body)
		assertPublicMessage(t, body)
	})

	t.Run("task-status", func(t *testing.T) {
		srv := httptest.NewServer(testServerWithGenerateError(t, raw))
		defer srv.Close()
		create, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/image-tasks/generations", strings.NewReader(`{"prompt":"draw"}`))
		create.Header.Set("Authorization", "Bearer k")
		created, err := http.DefaultClient.Do(create)
		if err != nil {
			t.Fatal(err)
		}
		var submitted tasks.Task
		if err := json.NewDecoder(created.Body).Decode(&submitted); err != nil {
			created.Body.Close()
			t.Fatal(err)
		}
		created.Body.Close()
		if submitted.ID == "" {
			t.Fatalf("submitted=%#v", submitted)
		}

		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			request, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/image-tasks/"+submitted.ID+"/status", nil)
			request.Header.Set("Authorization", "Bearer k")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			assertRedacted(t, body)
			var task tasks.Task
			if err := json.Unmarshal(body, &task); err != nil {
				t.Fatal(err)
			}
			if task.Status == tasks.StatusFailed {
				if task.Error != openaiweb.PublicCredentialInvalidMessage || task.RealtimeStatus != openaiweb.PublicCredentialInvalidMessage {
					t.Fatalf("task=%#v", task)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("task did not reach failed state")
	})
}

func TestCompactTaskListOmitsLogsAndLegacyAlias(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	create, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw","model":"gpt-image-2"}`))
	create.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(create)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	compactRequest, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/image-tasks?compact=1", nil)
	compactRequest.Header.Set("Authorization", "Bearer k")
	compactResponse, err := http.DefaultClient.Do(compactRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer compactResponse.Body.Close()
	var compactPayload struct {
		Items []tasks.Task    `json:"items"`
		Tasks json.RawMessage `json:"tasks"`
	}
	if err := json.NewDecoder(compactResponse.Body).Decode(&compactPayload); err != nil {
		t.Fatal(err)
	}
	if compactResponse.StatusCode != http.StatusOK || len(compactPayload.Items) != 1 {
		t.Fatalf("compact response status=%d payload=%#v", compactResponse.StatusCode, compactPayload)
	}
	if compactPayload.Tasks != nil || len(compactPayload.Items[0].StatusLogs) != 0 || compactPayload.Items[0].StatusLogCount == 0 {
		t.Fatalf("compact payload=%#v", compactPayload)
	}

	legacyRequest, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/image-tasks", nil)
	legacyRequest.Header.Set("Authorization", "Bearer k")
	legacyResponse, err := http.DefaultClient.Do(legacyRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyResponse.Body.Close()
	var legacyPayload struct {
		Items []tasks.Task    `json:"items"`
		Tasks json.RawMessage `json:"tasks"`
	}
	if err := json.NewDecoder(legacyResponse.Body).Decode(&legacyPayload); err != nil {
		t.Fatal(err)
	}
	if legacyResponse.StatusCode != http.StatusOK || len(legacyPayload.Items) != 1 || len(legacyPayload.Tasks) == 0 || len(legacyPayload.Items[0].StatusLogs) == 0 {
		t.Fatalf("legacy payload=%#v", legacyPayload)
	}
}

func TestCallLogsUseLogTypeAndDetailShape(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	request, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw","model":"gpt-image-2"}`))
	request.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("generation status=%d", response.StatusCode)
	}
	logsRequest, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/logs?type=call", nil)
	logsRequest.Header.Set("Authorization", "Bearer k")
	response, err = http.DefaultClient.Do(logsRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var logsPayload struct {
		Items []struct {
			Type   string         `json:"type"`
			Detail map[string]any `json:"detail"`
		} `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&logsPayload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(logsPayload.Items) != 1 || logsPayload.Items[0].Type != "call" || logsPayload.Items[0].Detail["model"] != "gpt-image-2" {
		t.Fatalf("log status=%d payload=%#v", response.StatusCode, logsPayload)
	}
}

func TestAccountImageTestCreatesTrackedTask(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	request, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/accounts/test-image", strings.NewReader(`{"access_token":"tok","model":"gpt-image-2"}`))
	request.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		OK     bool   `json:"ok"`
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !payload.OK || payload.TaskID == "" {
		t.Fatalf("status=%d payload=%#v", response.StatusCode, payload)
	}
	tasksRequest, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/image-tasks?ids="+payload.TaskID, nil)
	tasksRequest.Header.Set("Authorization", "Bearer k")
	tasksResponse, err := http.DefaultClient.Do(tasksRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer tasksResponse.Body.Close()
	var taskPayload struct {
		Items []tasks.Task `json:"items"`
	}
	if err := json.NewDecoder(tasksResponse.Body).Decode(&taskPayload); err != nil {
		t.Fatal(err)
	}
	if tasksResponse.StatusCode != http.StatusOK || len(taskPayload.Items) != 1 || taskPayload.Items[0].Status != tasks.StatusSucceeded {
		t.Fatalf("task status=%d payload=%#v", tasksResponse.StatusCode, taskPayload)
	}
}

func TestAccountImportRemovesInvalidTokens(t *testing.T) {
	cfg := config.Default()
	cfg.AuthKeyFile = filepath.Join(t.TempDir(), "auth-keys.json")
	cfg.ImageOutputDir = filepath.Join(t.TempDir(), "images")
	cfg.CallLogFile = filepath.Join(t.TempDir(), "calls.json")
	cfg.ImageTagsFile = filepath.Join(t.TempDir(), "tags.json")
	cfg.RegisterFile = filepath.Join(t.TempDir(), "register.json")
	backend := &validatingAPIBackend{readinessErrs: map[string]error{"bad": errors.New("token invalidated")}}
	srv := httptest.NewServer(newTestServerWithBackend(cfg, backend))
	defer srv.Close()

	request, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/accounts", strings.NewReader(`{"tokens":["good","bad"]}`))
	request.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Added     int                 `json:"added"`
		Refreshed int                 `json:"refreshed"`
		Errors    []map[string]string `json:"errors"`
		Items     []map[string]any    `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || payload.Added != 2 || payload.Refreshed != 1 || len(payload.Errors) != 1 || len(payload.Items) != 2 {
		t.Fatalf("status=%d payload=%#v", response.StatusCode, payload)
	}
	valid := 0
	for _, item := range payload.Items {
		if item["access_token"] == "good" {
			valid++
		}
		if item["access_token"] == "bad" {
			t.Fatalf("invalid account remained in pool: %#v", item)
		}
	}
	if valid != 1 || backend.readinessCheckCount() != 2 {
		t.Fatalf("valid=%d readinessChecks=%d payload=%#v", valid, backend.readinessCheckCount(), payload)
	}
}

func TestTaskEndpointDoesNotReuseClientTaskID(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	post := func() string {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/image-tasks/generations", strings.NewReader(`{"client_task_id":"same","prompt":"draw"}`))
		req.Header.Set("Authorization", "Bearer k")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var task map[string]any
		json.NewDecoder(resp.Body).Decode(&task)
		return task["id"].(string)
	}
	a, b := post(), post()
	if a == b {
		t.Fatal("client task id reused existing task")
	}
}

func TestChatCompletionsEndpoint(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	json.NewDecoder(resp.Body).Decode(&payload)
	choices, _ := payload["choices"].([]any)
	if resp.StatusCode != 200 || len(choices) != 1 {
		t.Fatalf("status=%d payload=%#v", resp.StatusCode, payload)
	}
}

func TestResponsesEndpoint(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"auto","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != 200 || payload["output_text"] != "hello" {
		t.Fatalf("status=%d payload=%#v", resp.StatusCode, payload)
	}
}

func TestAnthropicMessagesEndpoint(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != 200 || payload["type"] != "message" {
		t.Fatalf("status=%d payload=%#v", resp.StatusCode, payload)
	}
}

func TestSearchEndpoint(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/search", strings.NewReader(`{"prompt":"query"}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != 200 || payload["answer"] != "search answer" {
		t.Fatalf("status=%d payload=%#v", resp.StatusCode, payload)
	}
}

func TestImagesListUsesForwardedBaseURL(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/images", nil)
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "images.example.test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestUserKeyAccessAndAdminBoundary(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	create, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/users", strings.NewReader(`{"name":"Client"}`))
	create.Header.Set("Authorization", "Bearer k")
	created, err := http.DefaultClient.Do(create)
	if err != nil {
		t.Fatal(err)
	}
	defer created.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(created.Body).Decode(&payload)
	rawKey, _ := payload["key"].(string)
	if created.StatusCode != http.StatusOK || rawKey == "" {
		t.Fatalf("status=%d payload=%#v", created.StatusCode, payload)
	}

	image, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw","model":"gpt-image-2"}`))
	image.Header.Set("Authorization", "Bearer "+rawKey)
	response, err := http.DefaultClient.Do(image)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("user image status=%d", response.StatusCode)
	}

	adminOnly, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/accounts", nil)
	adminOnly.Header.Set("Authorization", "Bearer "+rawKey)
	response, err = http.DefaultClient.Do(adminOnly)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("admin endpoint status=%d", response.StatusCode)
	}
}

func TestOAuthStartEndpoint(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/accounts/oauth/start", strings.NewReader(`{"email_hint":"person@example.test"}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != http.StatusOK || payload["session_id"] == "" || !strings.Contains(payload["authorize_url"], "login_hint=person%40example.test") {
		t.Fatalf("status=%d payload=%#v", resp.StatusCode, payload)
	}
}

func TestChatGPTWebDebugEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte("<html></html>"))
			return
		}
		if r.URL.Path == "/backend-anon/models" {
			if r.Header.Get("X-OpenAI-Target-Path") != "/backend-anon/models?iim=false" {
				t.Errorf("target path=%q", r.Header.Get("X-OpenAI-Target-Path"))
			}
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5-5"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	cfg := config.Default()
	cfg.ChatGPTBaseURL = upstream.URL
	cfg.AuthKeyFile = filepath.Join(t.TempDir(), "auth-keys.json")
	cfg.ImageOutputDir = filepath.Join(t.TempDir(), "images")
	cfg.CallLogFile = filepath.Join(t.TempDir(), "calls.json")
	cfg.ImageTagsFile = filepath.Join(t.TempDir(), "tags.json")
	srv := httptest.NewServer(newTestServer(cfg))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/debug/chatgpt-web", strings.NewReader(`{"method":"GET","path":"/backend-anon/models?iim=false","bootstrap":true}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != http.StatusOK || payload["status"] != float64(http.StatusOK) || payload["ok"] != true {
		t.Fatalf("status=%d payload=%#v", resp.StatusCode, payload)
	}
}

func TestBarkTestEndpointReportsDisabledConfiguration(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/notifications/bark/test", nil)
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Result struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != http.StatusOK || payload.Result.OK || payload.Result.Error == "" {
		t.Fatalf("status=%d payload=%#v", resp.StatusCode, payload)
	}
}

func TestRegisterManagementEndpoints(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	request := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer k")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	update := request(http.MethodPost, "/api/register", `{"total":1,"threads":1,"mode":"total"}`)
	defer update.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(update.Body).Decode(&payload)
	if update.StatusCode != http.StatusOK {
		t.Fatalf("update=%d payload=%#v", update.StatusCode, payload)
	}
	get := request(http.MethodGet, "/api/register", "")
	defer get.Body.Close()
	_ = json.NewDecoder(get.Body).Decode(&payload)
	register, _ := payload["register"].(map[string]any)
	if register["total"] != float64(1) || register["threads"] != float64(1) || register["mode"] != "total" {
		t.Fatalf("register=%#v", register)
	}
	start := request(http.MethodPost, "/api/register/start", "")
	defer start.Body.Close()
	if start.StatusCode != http.StatusOK {
		t.Fatalf("start=%d", start.StatusCode)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		state := request(http.MethodGet, "/api/register", "")
		var data struct {
			Register struct {
				Enabled bool `json:"enabled"`
			} `json:"register"`
		}
		_ = json.NewDecoder(state.Body).Decode(&data)
		state.Body.Close()
		if !data.Register.Enabled {
			return
		}
	}
	t.Fatal("registration worker did not finish")
}

func TestUserTasksAreScopedToTheirAPIKey(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	createUser := func(name string) string {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/users", strings.NewReader(`{"name":"`+name+`"}`))
		req.Header.Set("Authorization", "Bearer k")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		key, _ := payload["key"].(string)
		if resp.StatusCode != http.StatusOK || key == "" {
			t.Fatalf("create user status=%d payload=%#v", resp.StatusCode, payload)
		}
		return key
	}
	postTask := func(key string) string {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/image-tasks/generations", strings.NewReader(`{"prompt":"draw"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var task map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&task)
		id, _ := task["id"].(string)
		if resp.StatusCode != http.StatusAccepted || id == "" {
			t.Fatalf("task status=%d payload=%#v", resp.StatusCode, task)
		}
		return id
	}
	keyA, keyB := createUser("A"), createUser("B")
	taskA, taskB := postTask(keyA), postTask(keyB)
	list, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/image-tasks", nil)
	list.Header.Set("Authorization", "Bearer "+keyA)
	resp, err := http.DefaultClient.Do(list)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != http.StatusOK || len(payload.Items) != 1 || payload.Items[0].ID != taskA {
		t.Fatalf("list status=%d payload=%#v", resp.StatusCode, payload)
	}
	foreign, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/image-tasks/"+taskB+"/status", nil)
	foreign.Header.Set("Authorization", "Bearer "+keyA)
	resp, err = http.DefaultClient.Do(foreign)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign status code=%d", resp.StatusCode)
	}
}

func TestSettingsPersistAndNotify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := config.LoadIfExists(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthKeyFile = filepath.Join(t.TempDir(), "auth-keys.json")
	cfg.ImageOutputDir = filepath.Join(t.TempDir(), "images")
	cfg.CallLogFile = filepath.Join(t.TempDir(), "calls.json")
	cfg.ImageTagsFile = filepath.Join(t.TempDir(), "tags.json")
	var updated config.Config
	srv := httptest.NewServer(newTestServer(cfg, func(next config.Config) { updated = next }))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/settings", strings.NewReader(`{"image_web_model_slug":"gpt-5-6","refresh_account_interval_minute":2,"refresh_account_concurrency":3}`))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || updated.ImageWebModelSlug != "gpt-5-6" || updated.RefreshAccountIntervalMinutes != 2 || updated.RefreshAccountConcurrency != 3 {
		t.Fatalf("status=%d updated=%#v", resp.StatusCode, updated)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ImageWebModelSlug != "gpt-5-6" || reloaded.RefreshAccountIntervalMinutes != 2 || reloaded.RefreshAccountConcurrency != 3 {
		t.Fatalf("persisted config=%#v", reloaded)
	}
}

func TestSettingsUpdateAdminKey(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()

	update, err := http.NewRequest(http.MethodPost, srv.URL+"/api/settings", strings.NewReader(`{"api_keys":["new-admin-key"]}`))
	if err != nil {
		t.Fatal(err)
	}
	update.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(update)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d", response.StatusCode)
	}

	oldKeyRequest, err := http.NewRequest(http.MethodGet, srv.URL+"/api/settings", nil)
	if err != nil {
		t.Fatal(err)
	}
	oldKeyRequest.Header.Set("Authorization", "Bearer k")
	response, err = http.DefaultClient.Do(oldKeyRequest)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old key status=%d", response.StatusCode)
	}

	newKeyRequest, err := http.NewRequest(http.MethodGet, srv.URL+"/api/settings", nil)
	if err != nil {
		t.Fatal(err)
	}
	newKeyRequest.Header.Set("Authorization", "Bearer new-admin-key")
	response, err = http.DefaultClient.Do(newKeyRequest)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("new key status=%d", response.StatusCode)
	}
}

func TestSettingsRejectsEmptyAdminKeys(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()

	update, err := http.NewRequest(http.MethodPost, srv.URL+"/api/settings", strings.NewReader(`{"api_keys":[" ",""]}`))
	if err != nil {
		t.Fatal(err)
	}
	update.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(update)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty key status=%d", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, srv.URL+"/api/settings", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer k")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("original key status=%d", response.StatusCode)
	}
}

func TestDashboardIncludesCallRuntime(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	request, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw","model":"gpt-image-2"}`))
	request.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("generation status=%d", response.StatusCode)
	}
	dashboard, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/dashboard", nil)
	dashboard.Header.Set("Authorization", "Bearer k")
	response, err = http.DefaultClient.Do(dashboard)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(response.Body).Decode(&payload)
	calls, _ := payload["calls"].(map[string]any)
	runtime, _ := calls["runtime"].(map[string]any)
	series, _ := runtime["series"].([]any)
	storagePayload, _ := payload["storage"].(map[string]any)
	health, _ := storagePayload["health"].(map[string]any)
	backend, _ := storagePayload["backend"].(map[string]any)
	system, _ := payload["system"].(map[string]any)
	cpu, _ := system["cpu"].(map[string]any)
	if response.StatusCode != http.StatusOK || len(series) != 60 || health["status"] != "healthy" || backend["type"] != "local" || backend["description"] != "本地文件存储" || cpu["cores"] == nil {
		t.Fatalf("status=%d payload=%#v", response.StatusCode, payload)
	}
}

func TestSystemLoadEndpointIsAuthenticated(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	response, err := http.Get(srv.URL + "/api/system/load")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}

	request, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/system/load", nil)
	request.Header.Set("Authorization", "Bearer k")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	cpu, _ := payload["cpu"].(map[string]any)
	memory, _ := payload["memory"].(map[string]any)
	disk, _ := payload["disk"].(map[string]any)
	network, _ := payload["network"].(map[string]any)
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || payload["sampled_at"] == nil || cpu["usage_percent"] == nil || cpu["load_15"] == nil || memory["available_bytes"] == nil || disk["path"] == nil || network["receive_bytes_per_second"] == nil {
		t.Fatalf("status=%d payload=%#v", response.StatusCode, payload)
	}
}

func TestDashboardSupportsRuntimeWindow(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	request, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/dashboard?runtime_window_minutes=10080", nil)
	request.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	calls, _ := payload["calls"].(map[string]any)
	runtime, _ := calls["runtime"].(map[string]any)
	series, _ := runtime["series"].([]any)
	if response.StatusCode != http.StatusOK || runtime["window_minutes"] != float64(10080) || runtime["bucket_minutes"] != float64(60) || len(series) != 168 {
		t.Fatalf("status=%d runtime=%#v", response.StatusCode, runtime)
	}
}

func TestProxyRuntimeEndpoint(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	request, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proxy/runtime", strings.NewReader(`{"enabled":true,"egress_mode":"single_proxy","proxy_url":"http://127.0.0.1:8081","clearance":{"enabled":false,"mode":"none"}}`))
	request.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(response.Body).Decode(&payload)
	runtime, _ := payload["runtime"].(map[string]any)
	if response.StatusCode != http.StatusOK || runtime["proxy_url"] != "http://127.0.0.1:8081" {
		t.Fatalf("status=%d payload=%#v", response.StatusCode, payload)
	}
}

func TestImageTagsAndThumbnailEndpoints(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	cfg.AuthKeyFile = filepath.Join(dir, "auth-keys.json")
	cfg.ImageTagsFile = filepath.Join(dir, "tags.json")
	cfg.ImageOutputDir = filepath.Join(dir, "images")
	cfg.CallLogFile = filepath.Join(dir, "calls.json")
	imagePath := filepath.Join(cfg.ImageOutputDir, "2026", "07", "11", "a.png")
	if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newTestServer(cfg))
	defer srv.Close()
	set, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/images/tags", strings.NewReader(`{"path":"2026/07/11/a.png","tags":["favorite"]}`))
	set.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(set)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("set tags status=%d", response.StatusCode)
	}
	list, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/images", nil)
	list.Header.Set("Authorization", "Bearer k")
	response, err = http.DefaultClient.Do(list)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.NewDecoder(response.Body).Decode(&payload)
	response.Body.Close()
	items, _ := payload["items"].([]any)
	if response.StatusCode != http.StatusOK || len(items) != 1 {
		t.Fatalf("status=%d payload=%#v", response.StatusCode, payload)
	}
	item, _ := items[0].(map[string]any)
	tags, _ := item["tags"].([]any)
	if len(tags) != 1 || item["thumbnail_url"] == "" {
		t.Fatalf("item=%#v", item)
	}
	thumb, err := http.Get(srv.URL + "/image-thumbnails/2026/07/11/a.png")
	if err != nil {
		t.Fatal(err)
	}
	thumb.Body.Close()
	if thumb.StatusCode != http.StatusOK {
		t.Fatalf("thumbnail status=%d", thumb.StatusCode)
	}
}
