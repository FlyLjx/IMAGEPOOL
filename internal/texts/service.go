package texts

import (
	"context"
	"fmt"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/openaiweb"
)

type Backend interface {
	GenerateText(ctx context.Context, account accounts.Account, req openaiweb.ChatRequest) (openaiweb.ChatResult, error)
	StreamText(ctx context.Context, account accounts.Account, req openaiweb.ChatRequest, emit func(openaiweb.ChatDelta) error) (string, error)
}

type Service struct {
	cfg     config.Config
	store   *accounts.Store
	backend Backend
}

type Result struct {
	Text           string                 `json:"text"`
	Model          string                 `json:"model"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	AccountEmail   string                 `json:"account_email,omitempty"`
	Attempts       []openaiweb.AttemptLog `json:"attempts,omitempty"`
}

func NewService(cfg config.Config, store *accounts.Store, backend Backend) *Service {
	return &Service{cfg: cfg.Normalize(), store: store, backend: backend}
}

func (s *Service) Generate(ctx context.Context, req openaiweb.ChatRequest) (Result, error) {
	result := Result{}
	err := s.withAccountRetry(ctx, func(account accounts.Account) error {
		out, err := s.backend.GenerateText(ctx, account, req)
		if err != nil {
			return err
		}
		result.Text = out.Text
		result.Model = out.Model
		result.ConversationID = out.ConversationID
		result.AccountEmail = out.AccountEmail
		return nil
	}, &result.Attempts)
	return result, err
}

func (s *Service) Stream(ctx context.Context, req openaiweb.ChatRequest, emit func(openaiweb.ChatDelta) error) (Result, error) {
	result := Result{Model: req.Model}
	err := s.withAccountRetry(ctx, func(account accounts.Account) error {
		var text string
		conv, err := s.backend.StreamText(ctx, account, req, func(delta openaiweb.ChatDelta) error {
			text += delta.Delta
			result.ConversationID = delta.ConversationID
			return emit(delta)
		})
		if err != nil {
			return err
		}
		result.Text = text
		result.ConversationID = conv
		result.AccountEmail = account.Email
		if result.Model == "" {
			result.Model = req.Model
		}
		if result.Model == "" {
			result.Model = "auto"
		}
		return nil
	}, &result.Attempts)
	return result, err
}

func (s *Service) withAccountRetry(ctx context.Context, fn func(accounts.Account) error, attempts *[]openaiweb.AttemptLog) error {
	exclude := map[string]bool{}
	maxAttempts := s.cfg.MaxImageAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		account, err := s.store.SelectForImage(exclude)
		if err != nil {
			if lastErr != nil {
				return fmt.Errorf("%w; attempts=%v", lastErr, *attempts)
			}
			return err
		}
		account, found, identityErr := s.store.EnsureBrowserIdentity(account.AccessToken)
		if identityErr != nil {
			return identityErr
		}
		if !found {
			return fmt.Errorf("account not found")
		}
		exclude[account.AccessToken] = true
		log := openaiweb.AttemptLog{Attempt: attempt, AccountEmail: account.Email, Status: "running"}
		err = fn(account)
		if err == nil {
			_ = s.store.MarkSuccess(account.AccessToken)
			log.Status = "success"
			*attempts = append(*attempts, log)
			return nil
		}
		lastErr = err
		_ = s.store.MarkFailure(account.AccessToken, err)
		log.Status = "failed"
		log.Error = err.Error()
		if openaiweb.IsAuthenticationError(err) {
			_, queued, _ := s.store.MarkTokenRecoveryPending(account.AccessToken, err.Error())
			log.RecoveryQueued = queued
		}
		*attempts = append(*attempts, log)
		if !openaiweb.IsAuthenticationError(err) {
			return err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("text generation failed")
	}
	return lastErr
}
