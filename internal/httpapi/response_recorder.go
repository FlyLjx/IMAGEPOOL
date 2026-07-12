package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

const maxRecordedResponseBody = 16 << 10

type responseRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (w *responseRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(data []byte) (int, error) {
	if w.body.Len() < maxRecordedResponseBody {
		remaining := maxRecordedResponseBody - w.body.Len()
		_, _ = w.body.Write(data[:min(len(data), remaining)])
	}
	return w.ResponseWriter.Write(data)
}

func (w *responseRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseRecorder) ErrorMessage() string {
	if w.status < http.StatusBadRequest || w.body.Len() == 0 {
		return ""
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(w.body.Bytes(), &payload) == nil && strings.TrimSpace(payload.Error.Message) != "" {
		return strings.TrimSpace(payload.Error.Message)
	}
	return strings.TrimSpace(w.body.String())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
