package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"imagepool/internal/updater"
)

func (s *Server) handleSystemUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, s.updater.Status())
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]any{"message": "method not allowed"}})
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, started, err := s.updater.Trigger(body.Version)
	if err != nil {
		statusCode := http.StatusServiceUnavailable
		if errors.Is(err, updater.ErrInvalidVersion) {
			statusCode = http.StatusBadRequest
		}
		writeError(w, statusCode, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started, "update": status})
}
