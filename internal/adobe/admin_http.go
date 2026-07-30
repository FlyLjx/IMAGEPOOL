package adobe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func (r *Runtime) ServeAdminHTTP(w http.ResponseWriter, req *http.Request) {
	requestID, _ := randomID("admin_", 8)
	switch {
	case req.URL.Path == "/api/adobe/routes" && req.Method == http.MethodGet:
		routes, err := r.repository.ListRoutes(req.Context())
		if err != nil {
			writeInternalError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Adobe routes could not be loaded", false, requestID)
			return
		}
		writeInternalJSON(w, http.StatusOK, map[string]any{"items": routes})
	case req.URL.Path == "/api/adobe/routes" && req.Method == http.MethodPost:
		var body RouteInput
		if err := decodeInternalJSON(req, &body); err != nil {
			writeInternalError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), false, requestID)
			return
		}
		route, err := r.repository.CreateRoute(req.Context(), body)
		if err != nil {
			writeInternalError(w, http.StatusBadRequest, "INVALID_ROUTE", err.Error(), false, requestID)
			return
		}
		writeInternalJSON(w, http.StatusCreated, map[string]any{"item": route})
	case strings.HasPrefix(req.URL.Path, "/api/adobe/routes/"):
		r.handleAdminRoute(w, req, requestID)
	case req.URL.Path == "/api/adobe/accounts" && req.Method == http.MethodGet:
		accounts, err := r.repository.ListAccounts(req.Context())
		if err != nil {
			writeInternalError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Adobe accounts could not be loaded", false, requestID)
			return
		}
		writeInternalJSON(w, http.StatusOK, map[string]any{"items": accounts})
	case req.URL.Path == "/api/adobe/accounts/import" && req.Method == http.MethodPost:
		r.handleAccountImport(w, req, requestID)
	case req.URL.Path == "/api/adobe/accounts/refresh-token/start" && req.Method == http.MethodPost:
		var body struct {
			AccountIDs []string `json:"account_ids"`
		}
		if err := decodeInternalJSON(req, &body); err != nil {
			writeInternalError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), false, requestID)
			return
		}
		job, err := r.StartTokenRefreshJob(body.AccountIDs)
		if err != nil {
			writeInternalError(w, http.StatusBadRequest, "TOKEN_REFRESH_START_FAILED", err.Error(), false, requestID)
			return
		}
		writeInternalJSON(w, http.StatusAccepted, map[string]any{"item": job})
	case strings.HasPrefix(req.URL.Path, "/api/adobe/token-refresh-jobs/") && req.Method == http.MethodGet:
		jobID := strings.Trim(strings.TrimPrefix(req.URL.Path, "/api/adobe/token-refresh-jobs/"), "/")
		job, ok := r.TokenRefreshJob(jobID)
		if !ok {
			writeInternalError(w, http.StatusNotFound, "TOKEN_REFRESH_JOB_NOT_FOUND", "Adobe Token refresh job not found", false, requestID)
			return
		}
		writeInternalJSON(w, http.StatusOK, map[string]any{"item": job})
	case strings.HasPrefix(req.URL.Path, "/api/adobe/test-image-jobs/") && req.Method == http.MethodGet:
		jobID := strings.Trim(strings.TrimPrefix(req.URL.Path, "/api/adobe/test-image-jobs/"), "/")
		job, ok := r.testImageJobs.Get(jobID)
		if !ok {
			writeInternalError(w, http.StatusNotFound, "TEST_IMAGE_JOB_NOT_FOUND", "Adobe test image job not found", false, requestID)
			return
		}
		writeInternalJSON(w, http.StatusOK, map[string]any{"item": job})
	case strings.HasPrefix(req.URL.Path, "/api/adobe/accounts/"):
		r.handleAdminAccount(w, req, requestID)
	default:
		writeInternalError(w, http.StatusNotFound, "NOT_FOUND", "Adobe admin endpoint not found", false, requestID)
	}
}

func (r *Runtime) handleAccountImport(w http.ResponseWriter, req *http.Request, requestID string) {
	raw, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
	if err != nil || len(raw) == 0 {
		writeInternalError(w, http.StatusBadRequest, "INVALID_REQUEST", "request body is required", false, requestID)
		return
	}
	result, importErr := r.ImportAdobe2API(req.Context(), raw)
	if importErr != nil && result.Total == 0 {
		writeInternalError(w, http.StatusBadRequest, "INVALID_ADOBE2API_IMPORT", importErr.Error(), false, requestID)
		return
	}
	payload := map[string]any{
		"status": result.Status, "total": result.Total, "imported_count": result.ImportedCount,
		"failed_count": result.FailedCount, "items": result.Items, "failures": result.Failures,
	}
	if len(result.Items) == 1 {
		payload["item"] = result.Items[0]
	}
	status := http.StatusAccepted
	if result.ImportedCount == 0 {
		status = http.StatusBadRequest
		message := "all Adobe account imports failed"
		if len(result.Failures) > 0 && strings.TrimSpace(result.Failures[0].Error) != "" {
			message = result.Failures[0].Error
		}
		payload["error"] = map[string]any{
			"code": "ADOBE_IMPORT_FAILED", "message": message, "retryable": false, "request_id": requestID,
		}
	}
	writeInternalJSON(w, status, payload)
}

func (r *Runtime) handleAdminRoute(w http.ResponseWriter, req *http.Request, requestID string) {
	remainder := strings.Trim(strings.TrimPrefix(req.URL.Path, "/api/adobe/routes/"), "/")
	parts := strings.Split(remainder, "/")
	routeID := strings.TrimSpace(parts[0])
	if routeID == "" {
		writeInternalError(w, http.StatusBadRequest, "ROUTE_ID_REQUIRED", "route id is required", false, requestID)
		return
	}
	if req.Method == http.MethodPost && len(parts) == 2 && parts[1] == "test" {
		ctx, cancel := context.WithTimeout(req.Context(), 25*time.Second)
		defer cancel()
		route, err := r.TestRoute(ctx, routeID)
		writeInternalJSON(w, http.StatusOK, map[string]any{"item": route, "ok": err == nil})
		return
	}
	if req.Method == http.MethodPatch && len(parts) == 1 {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := decodeInternalJSON(req, &body); err != nil {
			writeInternalError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), false, requestID)
			return
		}
		route, err := r.repository.SetRouteEnabled(req.Context(), routeID, body.Enabled)
		if err != nil {
			writeAdminRepositoryError(w, err, requestID)
			return
		}
		if !body.Enabled {
			_, _ = r.repository.ReassignAccountsFromRoute(req.Context(), routeID)
		}
		writeInternalJSON(w, http.StatusOK, map[string]any{"item": route})
		return
	}
	if req.Method == http.MethodDelete && len(parts) == 1 {
		if err := r.repository.DeleteRoute(req.Context(), routeID); err != nil {
			writeAdminRepositoryError(w, err, requestID)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeInternalError(w, http.StatusNotFound, "NOT_FOUND", "Adobe route action not found", false, requestID)
}

func (r *Runtime) handleAdminAccount(w http.ResponseWriter, req *http.Request, requestID string) {
	remainder := strings.Trim(strings.TrimPrefix(req.URL.Path, "/api/adobe/accounts/"), "/")
	parts := strings.Split(remainder, "/")
	accountID := strings.TrimSpace(parts[0])
	if accountID == "" {
		writeInternalError(w, http.StatusNotFound, "NOT_FOUND", "Adobe account action not found", false, requestID)
		return
	}
	if len(parts) == 1 && req.Method == http.MethodDelete {
		if err := r.repository.DeleteAccount(req.Context(), accountID); err != nil {
			writeAdminRepositoryError(w, err, requestID)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[1] == "refresh-token" && req.Method == http.MethodPost {
		ctx, cancel := context.WithTimeout(req.Context(), 45*time.Second)
		defer cancel()
		account, err := r.RefreshAccountToken(ctx, accountID)
		if err != nil {
			writeInternalError(w, http.StatusBadRequest, "TOKEN_REFRESH_FAILED", err.Error(), false, requestID)
			return
		}
		writeInternalJSON(w, http.StatusOK, map[string]any{"item": account, "status": "ok"})
		return
	}
	if len(parts) == 2 && parts[1] == "refresh-credits" && req.Method == http.MethodPost {
		ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
		defer cancel()
		account, err := r.RefreshAccountCredits(ctx, accountID)
		if err != nil {
			writeInternalError(w, http.StatusBadGateway, "CREDITS_REFRESH_FAILED", err.Error(), true, requestID)
			return
		}
		writeInternalJSON(w, http.StatusOK, map[string]any{"item": account, "status": "ok"})
		return
	}
	if len(parts) == 3 && parts[1] == "test-image" && parts[2] == "start" && req.Method == http.MethodPost {
		var body struct {
			Prompt  string `json:"prompt"`
			Model   string `json:"model"`
			Quality string `json:"quality"`
		}
		if err := decodeInternalJSON(req, &body); err != nil {
			writeInternalError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), false, requestID)
			return
		}
		body.Prompt, body.Model = strings.TrimSpace(body.Prompt), strings.TrimSpace(body.Model)
		if body.Prompt == "" || !IsModel(body.Model) {
			writeInternalError(w, http.StatusBadRequest, "INVALID_IMAGE_TEST", "prompt and a valid Adobe model are required", false, requestID)
			return
		}
		account, err := r.repository.GetAccount(req.Context(), accountID)
		if err != nil {
			writeAdminRepositoryError(w, err, requestID)
			return
		}
		if account.Disabled || account.State != "ready" {
			writeInternalError(w, http.StatusConflict, "ACCOUNT_NOT_READY", "Adobe account is not ready", false, requestID)
			return
		}
		job, created, err := r.StartTestImageJob(account, body.Prompt, body.Model, body.Quality)
		if err != nil {
			writeInternalError(w, http.StatusInternalServerError, "TEST_IMAGE_JOB_START_FAILED", err.Error(), true, requestID)
			return
		}
		status := http.StatusAccepted
		if !created {
			status = http.StatusOK
		}
		writeInternalJSON(w, status, map[string]any{"item": job, "created": created})
		return
	}
	if len(parts) == 2 && parts[1] == "disable" && req.Method == http.MethodPost {
		var body struct {
			Disabled bool `json:"disabled"`
		}
		if err := decodeInternalJSON(req, &body); err != nil {
			writeInternalError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), false, requestID)
			return
		}
		account, err := r.repository.SetAccountDisabled(req.Context(), accountID, body.Disabled)
		if err != nil {
			writeAdminRepositoryError(w, err, requestID)
			return
		}
		writeInternalJSON(w, http.StatusOK, map[string]any{"item": account})
		return
	}
	writeInternalError(w, http.StatusNotFound, "NOT_FOUND", "Adobe account action not found", false, requestID)
}

func writeAdminRepositoryError(w http.ResponseWriter, err error, requestID string) {
	if errors.Is(err, ErrNotFound) {
		writeInternalError(w, http.StatusNotFound, "NOT_FOUND", "Adobe resource not found", false, requestID)
		return
	}
	if errors.Is(err, ErrRouteInUse) {
		writeInternalError(w, http.StatusConflict, "ROUTE_IN_USE", err.Error(), false, requestID)
		return
	}
	if strings.Contains(err.Error(), "foreign key constraint") {
		writeInternalError(w, http.StatusConflict, "RESOURCE_IN_USE", "Adobe resource is in use", false, requestID)
		return
	}
	writeInternalError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Adobe operation failed", false, requestID)
}

func decodeInternalJSON(req *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(req.Body, 4<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeInternalJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeInternalError(w http.ResponseWriter, status int, code, message string, retryable bool, requestID string) {
	writeInternalJSON(w, status, map[string]any{"error": map[string]any{
		"code": code, "message": message, "retryable": retryable, "request_id": requestID,
	}})
}
