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

type recordedError struct {
	Message   string
	Code      string
	Title     string
	Category  string
	Retryable bool
	Action    string
	Hint      string
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

func (w *responseRecorder) ErrorInfo() recordedError {
	if w.status < http.StatusBadRequest || w.body.Len() == 0 {
		return recordedError{}
	}
	var payload struct {
		Error struct {
			Message   string `json:"message"`
			Code      string `json:"code"`
			Title     string `json:"title"`
			Category  string `json:"category"`
			Retryable bool   `json:"retryable"`
			Action    string `json:"action"`
			Hint      string `json:"hint"`
		} `json:"error"`
	}
	if json.Unmarshal(w.body.Bytes(), &payload) == nil && strings.TrimSpace(payload.Error.Message) != "" {
		return recordedError{
			Message: strings.TrimSpace(payload.Error.Message), Code: strings.TrimSpace(payload.Error.Code),
			Title: strings.TrimSpace(payload.Error.Title), Category: strings.TrimSpace(payload.Error.Category),
			Retryable: payload.Error.Retryable, Action: strings.TrimSpace(payload.Error.Action), Hint: strings.TrimSpace(payload.Error.Hint),
		}
	}
	return recordedError{Message: strings.TrimSpace(w.body.String())}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
