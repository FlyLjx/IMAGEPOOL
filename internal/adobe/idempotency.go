package adobe

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrIdempotencyConflict = errors.New("Idempotency-Key was already used with a different request")
	ErrRequestInProgress   = errors.New("idempotent request is still in progress")
)

type IdempotencyDecision struct {
	Execute      bool
	Replay       bool
	StatusCode   int
	ResponseBody json.RawMessage
}

func (r *Runtime) BeginIdempotentRequest(ctx context.Context, scope, key, requestHash string) (IdempotencyDecision, error) {
	scope = strings.TrimSpace(scope)
	key = strings.TrimSpace(key)
	requestHash = strings.TrimSpace(requestHash)
	if scope == "" || key == "" || requestHash == "" {
		return IdempotencyDecision{}, errors.New("idempotency scope, key and request hash are required")
	}
	if len(key) > 200 {
		return IdempotencyDecision{}, errors.New("Idempotency-Key must be at most 200 characters")
	}
	ttl := time.Duration(r.config.IdempotencyTTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	tx, err := r.repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IdempotencyDecision{}, err
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, `DELETE FROM adobe_idempotency_requests WHERE scope=$1 AND idempotency_key=$2 AND expires_at<=NOW()`, scope, key)
	result, err := tx.Exec(ctx, `
INSERT INTO adobe_idempotency_requests(scope,idempotency_key,request_hash,status,expires_at)
VALUES($1,$2,$3,'in_progress',NOW()+$4::interval)
ON CONFLICT(scope,idempotency_key) DO NOTHING`, scope, key, requestHash, ttl.String())
	if err != nil {
		return IdempotencyDecision{}, err
	}
	if result.RowsAffected() == 1 {
		if err := tx.Commit(ctx); err != nil {
			return IdempotencyDecision{}, err
		}
		return IdempotencyDecision{Execute: true}, nil
	}
	var storedHash, status string
	var responseStatus *int
	var responseBody []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,status,response_status,response_body FROM adobe_idempotency_requests WHERE scope=$1 AND idempotency_key=$2 FOR UPDATE`, scope, key).Scan(&storedHash, &status, &responseStatus, &responseBody)
	if err != nil {
		return IdempotencyDecision{}, err
	}
	if storedHash != requestHash {
		return IdempotencyDecision{}, ErrIdempotencyConflict
	}
	if status == "in_progress" {
		return IdempotencyDecision{}, ErrRequestInProgress
	}
	if responseStatus == nil || len(responseBody) == 0 {
		return IdempotencyDecision{}, ErrRequestInProgress
	}
	if err := tx.Commit(ctx); err != nil {
		return IdempotencyDecision{}, err
	}
	return IdempotencyDecision{Replay: true, StatusCode: *responseStatus, ResponseBody: append(json.RawMessage(nil), responseBody...)}, nil
}

func (r *Runtime) CompleteIdempotentRequest(ctx context.Context, scope, key string, statusCode int, response any) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	status := "completed"
	if statusCode >= 400 {
		status = "failed"
	}
	result, err := r.repository.db.Exec(ctx, `
UPDATE adobe_idempotency_requests SET status=$3,response_status=$4,response_body=$5::jsonb,updated_at=NOW()
WHERE scope=$1 AND idempotency_key=$2 AND status='in_progress'`, strings.TrimSpace(scope), strings.TrimSpace(key), status, statusCode, string(body))
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Runtime) CleanupIdempotency(ctx context.Context) error {
	_, err := r.repository.db.Exec(ctx, `DELETE FROM adobe_idempotency_requests WHERE expires_at<=NOW()`)
	return err
}
