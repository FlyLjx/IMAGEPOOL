package adobe

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type TokenRefreshEvent struct {
	AccountID string    `json:"account_id,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	At        time.Time `json:"at"`
}

type TokenRefreshJob struct {
	ID             string              `json:"id"`
	Status         string              `json:"status"`
	Total          int                 `json:"total"`
	Completed      int                 `json:"completed"`
	RefreshedCount int                 `json:"refreshed_count"`
	FailedCount    int                 `json:"failed_count"`
	Percent        int                 `json:"percent"`
	Message        string              `json:"message"`
	Events         []TokenRefreshEvent `json:"events"`
	StartedAt      time.Time           `json:"started_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
	FinishedAt     *time.Time          `json:"finished_at,omitempty"`
}

type tokenRefreshJobManager struct {
	mu   sync.RWMutex
	jobs map[string]*TokenRefreshJob
}

func newTokenRefreshJobManager() *tokenRefreshJobManager {
	return &tokenRefreshJobManager{jobs: make(map[string]*TokenRefreshJob)}
}

func (m *tokenRefreshJobManager) create(total int) (TokenRefreshJob, error) {
	if total <= 0 {
		return TokenRefreshJob{}, errors.New("no Adobe accounts selected for token refresh")
	}
	id, err := randomID("adobe_refresh_", 10)
	if err != nil {
		return TokenRefreshJob{}, err
	}
	now := time.Now().UTC()
	job := &TokenRefreshJob{ID: id, Status: "running", Total: total, Message: "Adobe Token 刷新任务已启动", StartedAt: now, UpdatedAt: now}
	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()
	return cloneTokenRefreshJob(job), nil
}

func (m *tokenRefreshJobManager) update(id string, mutate func(*TokenRefreshJob)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[id]; job != nil {
		mutate(job)
		job.UpdatedAt = time.Now().UTC()
	}
}

func (m *tokenRefreshJobManager) get(id string) (TokenRefreshJob, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job := m.jobs[strings.TrimSpace(id)]
	if job == nil {
		return TokenRefreshJob{}, false
	}
	return cloneTokenRefreshJob(job), true
}

func cloneTokenRefreshJob(job *TokenRefreshJob) TokenRefreshJob {
	if job == nil {
		return TokenRefreshJob{}
	}
	copyJob := *job
	copyJob.Events = append([]TokenRefreshEvent(nil), job.Events...)
	return copyJob
}

func (r *Runtime) StartTokenRefreshJob(accountIDs []string) (TokenRefreshJob, error) {
	ids := uniqueStrings(accountIDs)
	if len(ids) == 0 {
		var err error
		ids, err = r.repository.CompatibleRefreshAccountIDs(context.Background())
		if err != nil {
			return TokenRefreshJob{}, err
		}
	}
	job, err := r.tokenRefreshJobs.create(len(ids))
	if err != nil {
		return TokenRefreshJob{}, err
	}
	go r.runTokenRefreshJob(job.ID, ids)
	return job, nil
}

func (r *Runtime) TokenRefreshJob(jobID string) (TokenRefreshJob, bool) {
	if r == nil || r.tokenRefreshJobs == nil {
		return TokenRefreshJob{}, false
	}
	return r.tokenRefreshJobs.get(jobID)
}

func (r *Runtime) runTokenRefreshJob(jobID string, accountIDs []string) {
	for _, accountID := range accountIDs {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		_, err := r.RefreshAccountToken(ctx, accountID)
		cancel()
		r.tokenRefreshJobs.update(jobID, func(job *TokenRefreshJob) {
			job.Completed++
			status := "refreshed"
			message := "Token 已刷新"
			if err != nil {
				job.FailedCount++
				status = "failed"
				message = err.Error()
			} else {
				job.RefreshedCount++
			}
			job.Percent = job.Completed * 100 / job.Total
			job.Message = "正在刷新 Adobe Token"
			job.Events = append(job.Events, TokenRefreshEvent{AccountID: accountID, Status: status, Message: message, At: time.Now().UTC()})
		})
	}
	r.tokenRefreshJobs.update(jobID, func(job *TokenRefreshJob) {
		now := time.Now().UTC()
		job.FinishedAt = &now
		job.Percent = 100
		job.Status = "succeeded"
		job.Message = "Adobe Token 刷新完成"
		if job.FailedCount > 0 {
			job.Status = "partial"
			job.Message = "Adobe Token 刷新部分完成"
		}
		if job.RefreshedCount == 0 {
			job.Status = "failed"
			job.Message = "Adobe Token 刷新失败"
		}
	})
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func (r *Repository) CompatibleRefreshAccountIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT account_id FROM adobe_accounts WHERE registration_id LIKE 'adobe2api:cookie:%' AND disabled=FALSE ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
