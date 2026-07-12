package searches

import (
	"context"
	"fmt"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/openaiweb"
)

type Backend interface {
	Search(ctx context.Context, account accounts.Account, req openaiweb.SearchRequest) (openaiweb.SearchResult, error)
}

type Service struct {
	cfg     config.Config
	store   *accounts.Store
	backend Backend
}

func NewService(cfg config.Config, store *accounts.Store, backend Backend) *Service {
	return &Service{cfg: cfg.Normalize(), store: store, backend: backend}
}

func (s *Service) Search(ctx context.Context, prompt string) (openaiweb.SearchResult, error) {
	req := openaiweb.SearchRequest{Prompt: prompt, Model: s.cfg.SearchModel, TimeoutSecs: s.cfg.SearchTimeoutSecs, PollIntervalSecs: s.cfg.SearchPollIntervalSecs}
	exclude := map[string]bool{}
	maxAttempts := s.cfg.MaxImageAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		account, err := s.store.SelectForImage(exclude)
		if err != nil {
			if lastErr != nil {
				return openaiweb.SearchResult{}, fmt.Errorf("%w; no more search accounts", lastErr)
			}
			return openaiweb.SearchResult{}, err
		}
		account, found, identityErr := s.store.EnsureBrowserIdentity(account.AccessToken)
		if identityErr != nil {
			return openaiweb.SearchResult{}, identityErr
		}
		if !found {
			return openaiweb.SearchResult{}, fmt.Errorf("account not found")
		}
		exclude[account.AccessToken] = true
		result, err := s.backend.Search(ctx, account, req)
		if err == nil {
			_ = s.store.MarkSuccess(account.AccessToken)
			return result, nil
		}
		lastErr = err
		_ = s.store.MarkFailure(account.AccessToken, err)
		if openaiweb.IsAuthenticationError(err) {
			_, _ = s.store.RemoveInvalidToken(account.AccessToken, err.Error())
			continue
		}
		return openaiweb.SearchResult{}, err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("search failed")
	}
	return openaiweb.SearchResult{}, lastErr
}
