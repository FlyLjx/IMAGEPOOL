package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"imagepool/internal/auth"
	"imagepool/internal/images"
	"imagepool/internal/tasks"
)

func validateAsyncImageRequest(r *http.Request, req images.Request, inherentlyAsync bool) error {
	if req.Stream && (req.Async || inherentlyAsync) {
		return fmt.Errorf("stream and async cannot be enabled together")
	}
	callbackURL := strings.TrimSpace(req.CallbackURL)
	if callbackURL == "" {
		return nil
	}
	if !req.Async && !inherentlyAsync {
		return fmt.Errorf("callback_url requires async=true")
	}
	return validateCallbackURL(r.Context(), callbackURL)
}

func standardImageTask(task tasks.Task) map[string]any {
	status := "queued"
	switch task.Status {
	case tasks.StatusRunning:
		status = "in_progress"
	case tasks.StatusSucceeded:
		status = "completed"
	case tasks.StatusFailed:
		if task.Progress == "canceled" {
			status = "canceled"
		} else {
			status = "failed"
		}
	}
	result := map[string]any(nil)
	if status == "completed" {
		result = map[string]any{"created": task.CreatedAt.Unix(), "data": task.Data}
	}
	var taskError any
	if status == "failed" || status == "canceled" {
		taskError = map[string]any{
			"message": task.Error,
			"type":    task.ErrorCategory,
			"code":    task.ErrorCode,
		}
	}
	return map[string]any{
		"id":               task.ID,
		"object":           "image.task",
		"status":           status,
		"mode":             task.Mode,
		"model":            task.Model,
		"progress_percent": task.ProgressPercent,
		"created_at":       task.CreatedAt.Unix(),
		"updated_at":       task.UpdatedAt.Unix(),
		"result":           result,
		"error":            taskError,
	}
}

func (s *Server) handleStandardImageTask(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/images/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "task not found", "type": "invalid_request_error"}})
		return
	}
	identity, _ := auth.IdentityFromContext(r.Context())
	task, ok := s.tasks.StatusForOwner(id, identity.ID, identity.IsAdmin())
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "task not found", "type": "invalid_request_error"}})
		return
	}
	writeJSON(w, http.StatusOK, standardImageTask(publicTask(task)))
}
