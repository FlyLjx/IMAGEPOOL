package httpapi

import (
	"net/http"
	"time"

	"imagepool/internal/accounts"
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
