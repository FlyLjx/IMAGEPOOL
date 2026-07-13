package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type AccountChecker interface {
	CheckAccount(ctx context.Context, token string) (AccountCheckResult, error)
}

// LightweightAccountChecker is used by the periodic scheduler. It confirms
// account metadata and quota without exercising image-generation-only flows.
type LightweightAccountChecker interface {
	CheckAccountLight(ctx context.Context, token string) (AccountCheckResult, error)
}

type AccountCheckResult struct {
	Models            []string         `json:"models"`
	Email             string           `json:"email,omitempty"`
	Type              string           `json:"type,omitempty"`
	Quota             int              `json:"quota"`
	ImageQuotaUnknown bool             `json:"image_quota_unknown"`
	LimitsProgress    []map[string]any `json:"limits_progress,omitempty"`
	RestoreAt         string           `json:"restore_at,omitempty"`
	DefaultModelSlug  string           `json:"default_model_slug,omitempty"`
}

type RefreshItem struct {
	Token  string `json:"token"`
	Email  string `json:"email,omitempty"`
	Status string `json:"status"`
	Quota  int    `json:"quota,omitempty"`
	Error  string `json:"error,omitempty"`
}

type RefreshProgress struct {
	Total        int            `json:"total"`
	Processed    int            `json:"processed"`
	Done         bool           `json:"done"`
	Error        string         `json:"error,omitempty"`
	StatusCounts map[string]int `json:"status_counts"`
	TotalQuota   int            `json:"total_quota"`
	Results      []RefreshItem  `json:"results"`
}

type RefreshManager struct {
	store       *Store
	checker     AccountChecker
	concurrency int
	mu          sync.RWMutex
	sequence    uint64
	progress    map[string]*RefreshProgress
}

func NewRefreshManager(store *Store, checker AccountChecker, concurrency int) *RefreshManager {
	return &RefreshManager{store: store, checker: checker, concurrency: normalizeRefreshConcurrency(concurrency), progress: map[string]*RefreshProgress{}}
}

func (m *RefreshManager) SetConcurrency(concurrency int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.concurrency = normalizeRefreshConcurrency(concurrency)
	m.mu.Unlock()
}

func (m *RefreshManager) Start(tokens []string) (string, error) {
	if m == nil || m.store == nil || m.checker == nil {
		return "", fmt.Errorf("account refresh is not configured")
	}
	tokens = uniqueTokens(tokens)
	if len(tokens) == 0 {
		return "", fmt.Errorf("access_tokens is required")
	}
	m.mu.Lock()
	m.sequence++
	id := fmt.Sprintf("refresh_%d_%d", time.Now().UnixNano(), m.sequence)
	m.progress[id] = &RefreshProgress{Total: len(tokens), StatusCounts: map[string]int{}, Results: []RefreshItem{}}
	m.mu.Unlock()
	go m.run(id, tokens)
	return id, nil
}

func (m *RefreshManager) Get(id string) (RefreshProgress, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	progress := m.progress[strings.TrimSpace(id)]
	if progress == nil {
		return RefreshProgress{}, false
	}
	copy := *progress
	copy.StatusCounts = copyCounts(progress.StatusCounts)
	copy.Results = append([]RefreshItem(nil), progress.Results...)
	return copy, true
}

// RefreshNow validates accounts before returning. Import flows use this so a
// newly added token cannot be dispatched before its first validation finishes.
func (m *RefreshManager) RefreshNow(tokens []string) (RefreshProgress, error) {
	if m == nil || m.store == nil || m.checker == nil {
		return RefreshProgress{}, fmt.Errorf("account refresh is not configured")
	}
	tokens = uniqueTokens(tokens)
	if len(tokens) == 0 {
		return RefreshProgress{}, fmt.Errorf("access_tokens is required")
	}
	progress := RefreshProgress{Total: len(tokens), StatusCounts: map[string]int{}, Results: []RefreshItem{}}
	for result := range m.refreshResults(tokens, false) {
		progress.Processed++
		progress.Results = append(progress.Results, result)
		progress.StatusCounts[result.Status]++
		progress.TotalQuota += result.Quota
	}
	progress.Done = true
	return progress, nil
}

// RefreshScheduled performs a lower-impact refresh suitable for the periodic
// background job. Checkers without a lightweight implementation retain the
// full validation behavior.
func (m *RefreshManager) RefreshScheduled(tokens []string) (RefreshProgress, error) {
	if m == nil || m.store == nil || m.checker == nil {
		return RefreshProgress{}, fmt.Errorf("account refresh is not configured")
	}
	tokens = uniqueTokens(tokens)
	if len(tokens) == 0 {
		return RefreshProgress{}, fmt.Errorf("access_tokens is required")
	}
	progress := RefreshProgress{Total: len(tokens), StatusCounts: map[string]int{}, Results: []RefreshItem{}}
	for result := range m.refreshResults(tokens, true) {
		progress.Processed++
		progress.Results = append(progress.Results, result)
		progress.StatusCounts[result.Status]++
		progress.TotalQuota += result.Quota
	}
	progress.Done = true
	return progress, nil
}

func (m *RefreshManager) run(id string, tokens []string) {
	for result := range m.refreshResults(tokens, false) {
		m.mu.Lock()
		progress := m.progress[id]
		if progress != nil {
			progress.Processed++
			progress.Results = append(progress.Results, result)
			progress.StatusCounts[result.Status]++
			progress.TotalQuota += result.Quota
		}
		m.mu.Unlock()
	}
	m.mu.Lock()
	if progress := m.progress[id]; progress != nil {
		progress.Done = true
	}
	m.mu.Unlock()
}

func (m *RefreshManager) refreshResults(tokens []string, lightweight bool) <-chan RefreshItem {
	jobs := make(chan string)
	results := make(chan RefreshItem, len(tokens))
	m.mu.RLock()
	workers := m.concurrency
	m.mu.RUnlock()
	if workers > len(tokens) {
		workers = len(tokens)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for token := range jobs {
				results <- m.refreshOne(token, lightweight)
			}
		}()
	}
	go func() {
		for _, token := range tokens {
			jobs <- token
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	return results
}

func normalizeRefreshConcurrency(concurrency int) int {
	if concurrency <= 0 {
		return 8
	}
	if concurrency > 100 {
		return 100
	}
	return concurrency
}

func (m *RefreshManager) refreshOne(token string, lightweight bool) RefreshItem {
	account, _ := m.store.Get(token)
	item := RefreshItem{Token: token, Email: account.Email}
	var check AccountCheckResult
	var err error
	if lightweight {
		if checker, ok := m.checker.(LightweightAccountChecker); ok {
			check, err = checker.CheckAccountLight(context.Background(), token)
		} else {
			check, err = m.checker.CheckAccount(context.Background(), token)
		}
	} else {
		check, err = m.checker.CheckAccount(context.Background(), token)
	}
	if err != nil {
		if isAuthenticationFailureMessage(err.Error()) {
			removed, removeErr := m.store.RemoveInvalidToken(token, err.Error())
			if removeErr != nil {
				item.Error = removeErr.Error()
				item.Status = "error"
				return item
			}
			if removed {
				item.Status = "removed"
				item.Error = compactRefreshError(err)
				return item
			}
		}
		refreshErr := errors.New(compactRefreshError(err))
		updated, found, updateErr := m.store.RecordRefresh(token, AccountCheckResult{}, refreshErr)
		if updateErr != nil {
			item.Error = updateErr.Error()
		} else if found {
			item.Email = updated.Email
			item.Quota = updated.Quota
		}
		item.Status = "error"
		if item.Error == "" {
			item.Error = refreshErr.Error()
		}
		return item
	}
	updated, found, updateErr := m.store.RecordRefresh(token, check, nil)
	if updateErr != nil {
		return RefreshItem{Token: token, Email: account.Email, Status: "error", Error: updateErr.Error()}
	}
	if found {
		item.Email = updated.Email
		item.Quota = updated.Quota
	}
	item.Status = "success"
	return item
}

func compactRefreshError(err error) string {
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	if strings.Contains(lower, "upstream bootstrap status=403") || strings.Contains(lower, "cf-chl") {
		return "ChatGPT upstream blocked by Cloudflare (HTTP 403)"
	}
	if len(message) > 500 {
		return message[:500] + "..."
	}
	return message
}

func uniqueTokens(tokens []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

func copyCounts(counts map[string]int) map[string]int {
	copy := map[string]int{}
	for key, value := range counts {
		copy[key] = value
	}
	return copy
}

func isInvalidTokenMessage(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "token_revoked") || strings.Contains(value, "token_invalidated") || strings.Contains(value, "token invalidated") || strings.Contains(value, "authentication token has been invalidated") || strings.Contains(value, "invalidated oauth token")
}

func isAuthenticationFailureMessage(value string) bool {
	lower := strings.ToLower(value)
	return isInvalidTokenMessage(lower) || strings.Contains(lower, "status=401") || strings.Contains(lower, "http 401") || strings.Contains(lower, "http status 401")
}
