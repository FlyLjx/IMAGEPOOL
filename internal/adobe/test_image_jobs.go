package adobe

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

type TestImageJobEvent struct {
	Stage   string    `json:"stage"`
	Message string    `json:"message"`
	Percent int       `json:"percent"`
	At      time.Time `json:"at"`
}

type TestImageJob struct {
	ID            string              `json:"id"`
	AccountID     string              `json:"account_id"`
	Model         string              `json:"model"`
	AspectRatio   string              `json:"aspect_ratio,omitempty"`
	Status        string              `json:"status"`
	Stage         string              `json:"stage"`
	Message       string              `json:"message"`
	Percent       int                 `json:"percent"`
	UpstreamJobID string              `json:"upstream_job_id,omitempty"`
	ImageDataURL  string              `json:"image_data_url,omitempty"`
	Error         string              `json:"error,omitempty"`
	StartedAt     time.Time           `json:"started_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	FinishedAt    *time.Time          `json:"finished_at,omitempty"`
	Events        []TestImageJobEvent `json:"events"`
}

type testImageJobManager struct {
	mu        sync.RWMutex
	jobs      map[string]*TestImageJob
	byAccount map[string]string
	now       func() time.Time
}

func newTestImageJobManager() *testImageJobManager {
	return &testImageJobManager{jobs: map[string]*TestImageJob{}, byAccount: map[string]string{}, now: time.Now}
}

func (m *testImageJobManager) Start(accountID, model, aspectRatio string) (TestImageJob, bool, error) {
	accountID, model, aspectRatio = strings.TrimSpace(accountID), strings.TrimSpace(model), strings.TrimSpace(aspectRatio)
	if accountID == "" || model == "" {
		return TestImageJob{}, false, errors.New("account_id and model are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked()
	if jobID := m.byAccount[accountID]; jobID != "" {
		if job := m.jobs[jobID]; job != nil && job.Status == "running" {
			return cloneTestImageJob(job), false, nil
		}
	}
	id, err := randomID("adobetest_", 12)
	if err != nil {
		return TestImageJob{}, false, err
	}
	now := m.now().UTC()
	job := &TestImageJob{ID: id, AccountID: accountID, Model: PublicModelID(model), AspectRatio: aspectRatio, Status: "running", Stage: "queued", Message: "测试生图任务已排队", Percent: 1, StartedAt: now, UpdatedAt: now}
	job.Events = append(job.Events, TestImageJobEvent{Stage: job.Stage, Message: job.Message, Percent: job.Percent, At: now})
	m.jobs[id], m.byAccount[accountID] = job, id
	return cloneTestImageJob(job), true, nil
}

func (m *testImageJobManager) Update(jobID, stage, message string, percent int, details map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[jobID]
	if job == nil || job.Status != "running" {
		return
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	now := m.now().UTC()
	job.Stage, job.Message, job.Percent, job.UpdatedAt = strings.TrimSpace(stage), strings.TrimSpace(message), percent, now
	if upstreamID := strings.TrimSpace(stringValue(details["upstream_job_id"])); upstreamID != "" {
		job.UpstreamJobID = upstreamID
	}
	if len(job.Events) == 0 || job.Events[len(job.Events)-1].Stage != job.Stage || job.Events[len(job.Events)-1].Message != job.Message {
		job.Events = append(job.Events, TestImageJobEvent{Stage: job.Stage, Message: job.Message, Percent: job.Percent, At: now})
		if len(job.Events) > 40 {
			job.Events = append([]TestImageJobEvent(nil), job.Events[len(job.Events)-40:]...)
		}
	}
}

func (m *testImageJobManager) Succeed(jobID string, result ImageGenerateResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[jobID]
	if job == nil || job.Status != "running" {
		return
	}
	now := m.now().UTC()
	job.Status, job.Stage, job.Message, job.Percent = "succeeded", "completed", "测试图片生成完成", 100
	job.UpstreamJobID = result.UpstreamJobID
	if len(result.Images) > 0 {
		mimeType := http.DetectContentType(result.Images[0])
		job.ImageDataURL = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(result.Images[0])
	}
	job.UpdatedAt, job.FinishedAt = now, &now
	job.Events = append(job.Events, TestImageJobEvent{Stage: job.Stage, Message: job.Message, Percent: 100, At: now})
	delete(m.byAccount, job.AccountID)
}

func (m *testImageJobManager) Fail(jobID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[jobID]
	if job == nil || job.Status != "running" {
		return
	}
	now := m.now().UTC()
	job.Status, job.Stage, job.Message, job.Percent = "failed", "failed", "测试生图失败", 100
	job.Error = truncateError(err)
	job.UpdatedAt, job.FinishedAt = now, &now
	job.Events = append(job.Events, TestImageJobEvent{Stage: job.Stage, Message: job.Message, Percent: 100, At: now})
	delete(m.byAccount, job.AccountID)
}

func (m *testImageJobManager) Get(jobID string) (TestImageJob, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job := m.jobs[strings.TrimSpace(jobID)]
	if job == nil {
		return TestImageJob{}, false
	}
	return cloneTestImageJob(job), true
}

func (m *testImageJobManager) cleanupLocked() {
	cutoff := m.now().UTC().Add(-30 * time.Minute)
	for id, job := range m.jobs {
		if job.FinishedAt != nil && job.FinishedAt.Before(cutoff) {
			delete(m.jobs, id)
		}
	}
}

func cloneTestImageJob(job *TestImageJob) TestImageJob {
	if job == nil {
		return TestImageJob{}
	}
	clone := *job
	clone.Events = append([]TestImageJobEvent(nil), job.Events...)
	return clone
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func testImageProgressMessage(stage string) string {
	switch strings.TrimSpace(stage) {
	case "uploading":
		return "上传参考图片"
	case "starting_generation":
		return "向 Adobe 提交生图任务"
	case "polling_image":
		return "等待 Adobe 生成图片"
	case "downloading_image":
		return "下载生成结果"
	default:
		return "执行 Adobe 测试生图"
	}
}

func (r *Runtime) StartTestImageJob(account Account, prompt, model, aspectRatio, quality string) (TestImageJob, bool, error) {
	resolved, err := ResolveRequestedModel(model, aspectRatio)
	if err != nil {
		return TestImageJob{}, false, err
	}
	model, aspectRatio = resolved.ID, resolved.AspectRatio
	job, created, err := r.testImageJobs.Start(account.AccountID, model, aspectRatio)
	if err != nil || !created {
		return job, created, err
	}
	go func(jobID string, account Account) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(r.config.GenerateTimeoutSecs+30)*time.Second)
		defer cancel()
		result, generateErr := r.GenerateImageWithAccount(ctx, account.AccountID, ImageGenerateRequest{
			Prompt: prompt, Model: model, AspectRatio: aspectRatio, Quality: quality,
			Progress: func(stage string, percent int, details map[string]any) {
				r.testImageJobs.Update(jobID, stage, testImageProgressMessage(stage), percent, details)
			},
		})
		if generateErr != nil {
			r.testImageJobs.Fail(jobID, generateErr)
			return
		}
		r.testImageJobs.Succeed(jobID, result)
	}(job.ID, account)
	return job, true, nil
}
