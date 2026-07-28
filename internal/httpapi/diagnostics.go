package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/auth"
	"imagepool/internal/postprocess"
	"imagepool/internal/tasks"
)

type schedulerDiagnostics struct {
	GeneratedAt time.Time                   `json:"generated_at"`
	Tasks       tasks.Stats                 `json:"tasks"`
	GPT         accounts.ImageDispatchStats `json:"gpt"`
	Postprocess postprocess.Stats           `json:"postprocess"`
	Callbacks   callbackSnapshot            `json:"callbacks"`
}

func (s *Server) handlePostprocessTaskHistory(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page")))
	pageSize, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page_size")))
	identity, _ := auth.IdentityFromContext(r.Context())
	if s.postprocess == nil {
		if page <= 0 {
			page = 1
		}
		if pageSize <= 0 {
			pageSize = 50
		}
		if pageSize > 200 {
			pageSize = 200
		}
		writeJSON(w, http.StatusOK, postprocess.HistoryPage{Items: []postprocess.Task{}, Page: page, PageSize: pageSize})
		return
	}
	history, err := s.postprocess.History(page, pageSize, identity.ID, identity.IsAdmin())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, history)
}

func (s *Server) handleSchedulerDiagnostics(w http.ResponseWriter, _ *http.Request) {
	response := schedulerDiagnostics{GeneratedAt: time.Now(), Callbacks: s.callbacks.snapshot()}
	if s.tasks != nil {
		response.Tasks = s.tasks.Stats()
	}
	if s.accounts != nil {
		response.GPT = s.accounts.ImageDispatchStats()
	}
	if s.postprocess != nil {
		response.Postprocess = s.postprocess.Stats()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}
