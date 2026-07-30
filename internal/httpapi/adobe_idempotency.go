package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"imagepool/internal/adobe"
	"imagepool/internal/auth"
	"imagepool/internal/errorinfo"
	"imagepool/internal/images"
)

type adobeIdempotencyHandle struct {
	scope string
	key   string
}

func (s *Server) beginAdobeImageIdempotency(w http.ResponseWriter, r *http.Request, endpoint string, request images.Request) (*adobeIdempotencyHandle, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || s.adobe == nil || !adobe.IsRequestedModel(request.Model) {
		return nil, true
	}
	if request.Stream {
		writeError(w, http.StatusBadRequest, errors.New("Idempotency-Key is not supported with stream=true"))
		return nil, false
	}
	identity, _ := auth.IdentityFromContext(r.Context())
	scope := identity.ID + "|" + endpoint
	hash := imageRequestHash(request)
	decision, err := s.adobe.BeginIdempotentRequest(r.Context(), scope, key, hash)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, adobe.ErrIdempotencyConflict) || errors.Is(err, adobe.ErrRequestInProgress) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return nil, false
	}
	if decision.Replay {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Idempotency-Replayed", "true")
		w.WriteHeader(decision.StatusCode)
		_, _ = w.Write(decision.ResponseBody)
		return nil, false
	}
	return &adobeIdempotencyHandle{scope: scope, key: key}, true
}

func imageRequestHash(request images.Request) string {
	references := make([]string, 0, len(request.References))
	for _, reference := range request.References {
		digest := sha256.Sum256(reference.Data)
		references = append(references, hex.EncodeToString(digest[:]))
	}
	payload := map[string]any{
		"prompt": request.Prompt, "model": request.Model, "quality": request.Quality,
		"n": request.N, "response_format": request.ResponseFormat, "output_format": request.OutputFormat, "references": references,
	}
	if !adobe.IsRequestedModel(request.Model) {
		payload["size"] = request.Size
	}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (s *Server) completeAdobeIdempotency(r *http.Request, handle *adobeIdempotencyHandle, status int, payload any) {
	if handle == nil || s.adobe == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	_ = s.adobe.CompleteIdempotentRequest(ctx, handle.scope, handle.key, status, payload)
}

func (s *Server) writeAdobeIdempotentError(w http.ResponseWriter, r *http.Request, handle *adobeIdempotencyHandle, status int, err error) {
	if handle == nil {
		writeError(w, status, err)
		return
	}
	classified := errorinfo.Classify(err, status)
	if status <= 0 {
		status = classified.HTTPStatus
	}
	payload := map[string]any{"error": map[string]any{
		"message": classified.Message, "title": classified.Title, "type": classified.Type,
		"code": classified.Code, "category": classified.Category, "retryable": classified.Retryable,
		"action": classified.Action, "hint": classified.Hint, "request_id": responseID("err"),
	}}
	s.completeAdobeIdempotency(r, handle, status, payload)
	writeJSON(w, status, payload)
}
