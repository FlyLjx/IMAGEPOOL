package tasks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"imagepool/internal/images"
	"imagepool/internal/openaiweb"
	"imagepool/internal/persistence"
)

const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "success"
	StatusFailed    = "error"
	StatusCanceled  = "error"
	taskCollection  = "tasks"
	persistDebounce = 100 * time.Millisecond
)

type ImageService interface {
	Generate(ctx context.Context, req images.Request) (images.Response, error)
	GenerateWithAccount(ctx context.Context, token string, req images.Request) (images.Response, error)
}

type imageGenerator func(context.Context, images.Request) (images.Response, error)

type LogEntry struct {
	Time     time.Time      `json:"time"`
	Level    string         `json:"level,omitempty"`
	Event    string         `json:"event,omitempty"`
	Progress string         `json:"progress,omitempty"`
	Message  string         `json:"message,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

type Task struct {
	ID                     string           `json:"id"`
	OwnerID                string           `json:"owner_id,omitempty"`
	ClientTaskID           string           `json:"client_task_id,omitempty"`
	Mode                   string           `json:"mode"`
	Status                 string           `json:"status"`
	Progress               string           `json:"progress,omitempty"`
	ProgressPercent        int              `json:"progress_percent"`
	RealtimeStatus         string           `json:"realtime_status,omitempty"`
	Prompt                 string           `json:"prompt,omitempty"`
	Model                  string           `json:"model,omitempty"`
	Size                   string           `json:"size,omitempty"`
	Quality                string           `json:"quality,omitempty"`
	CreatedAt              time.Time        `json:"created_at"`
	StartedAt              *time.Time       `json:"started_at,omitempty"`
	FinishedAt             *time.Time       `json:"finished_at,omitempty"`
	UpdatedAt              time.Time        `json:"updated_at"`
	Result                 *images.Response `json:"-"`
	Data                   []images.Data    `json:"data,omitempty"`
	ConversationID         string           `json:"conversation_id,omitempty"`
	UsedAccountCount       int              `json:"used_account_count"`
	FailedAccountCount     int              `json:"failed_account_count"`
	ImageRouteAttemptCount int              `json:"image_route_attempt_count"`
	DurationMS             int64            `json:"duration_ms,omitempty"`
	ElapsedSecs            float64          `json:"elapsed_secs,omitempty"`
	Error                  string           `json:"error,omitempty"`
	StatusLogCount         int              `json:"status_log_count"`
	StatusLogs             []LogEntry       `json:"status_logs,omitempty"`
}

type Manager struct {
	mu      sync.RWMutex
	seq     uint64
	service ImageService
	state   persistence.Store
	items   persistence.CollectionStore
	tasks   map[string]*Task
	cancels map[string]context.CancelFunc
	dirty   map[string]struct{}
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	close   sync.Once
}

func NewManager(service ImageService) *Manager {
	return newManager(service, nil)
}

func NewManagerWithPersistence(service ImageService, state persistence.Store) *Manager {
	return newManager(service, state)
}

func newManager(service ImageService, state persistence.Store) *Manager {
	m := &Manager{service: service, state: state, tasks: map[string]*Task{}, cancels: map[string]context.CancelFunc{}, dirty: map[string]struct{}{}}
	if collection, ok := state.(persistence.CollectionStore); ok {
		m.items = collection
	}
	if state != nil {
		m.wake = make(chan struct{}, 1)
		m.stop = make(chan struct{})
		m.done = make(chan struct{})
	}
	m.load()
	if state != nil {
		go m.persistenceLoop()
		if len(m.dirty) > 0 {
			m.signalPersistence()
		}
	}
	return m
}

func (m *Manager) SubmitGeneration(clientTaskID string, req images.Request) Task {
	return m.SubmitGenerationForOwner("", clientTaskID, req)
}

func (m *Manager) SubmitEdit(clientTaskID string, req images.Request) Task {
	return m.SubmitEditForOwner("", clientTaskID, req)
}

func (m *Manager) SubmitGenerationForOwner(ownerID, clientTaskID string, req images.Request) Task {
	return m.submit("generate", ownerID, clientTaskID, req)
}

func (m *Manager) SubmitEditForOwner(ownerID, clientTaskID string, req images.Request) Task {
	return m.submit("edit", ownerID, clientTaskID, req)
}

func (m *Manager) RunGenerationForOwner(ctx context.Context, ownerID string, req images.Request) (Task, images.Response, error) {
	return m.runSync(ctx, "generate", ownerID, req)
}

func (m *Manager) RunEditForOwner(ctx context.Context, ownerID string, req images.Request) (Task, images.Response, error) {
	return m.runSync(ctx, "edit", ownerID, req)
}

func (m *Manager) RunGenerationWithAccountForOwner(ctx context.Context, ownerID, accessToken string, req images.Request) (Task, images.Response, error) {
	return m.runSyncWith(ctx, "generate", ownerID, req, func(runCtx context.Context, runReq images.Request) (images.Response, error) {
		return m.service.GenerateWithAccount(runCtx, accessToken, runReq)
	})
}

func (m *Manager) submit(mode, ownerID, clientTaskID string, req images.Request) Task {
	task, ctx := m.create(mode, ownerID, clientTaskID, req, context.Background())
	go func() {
		_, _ = m.run(ctx, task.ID, req)
	}()
	return task
}

func (m *Manager) runSync(ctx context.Context, mode, ownerID string, req images.Request) (Task, images.Response, error) {
	return m.runSyncWith(ctx, mode, ownerID, req, m.service.Generate)
}

func (m *Manager) runSyncWith(ctx context.Context, mode, ownerID string, req images.Request, generate imageGenerator) (Task, images.Response, error) {
	task, runCtx := m.create(mode, ownerID, "", req, ctx)
	result, err := m.runWith(runCtx, task.ID, req, generate)
	final, ok := m.Status(task.ID)
	if !ok {
		return task, result, err
	}
	return final, result, err
}

func (m *Manager) create(mode, ownerID, clientTaskID string, req images.Request, parent context.Context) (Task, context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("img_%d_%d", time.Now().UnixNano(), m.seq)
	now := time.Now()
	task := &Task{ID: id, OwnerID: strings.TrimSpace(ownerID), ClientTaskID: clientTaskID, Mode: mode, Status: StatusQueued, Progress: "queued", ProgressPercent: 0, RealtimeStatus: "任务已提交", Prompt: req.Prompt, Model: req.Model, Size: req.Size, Quality: req.Quality, CreatedAt: now, UpdatedAt: now}
	appendLog(task, LogEntry{Time: now, Level: "info", Event: "submitted", Progress: "queued", Message: "任务已提交"})
	m.tasks[id] = task
	m.markDirtyLocked(id)
	ctx, cancel := context.WithCancel(parent)
	m.cancels[id] = cancel
	snapshot := m.copyTask(task)
	m.mu.Unlock()
	return snapshot, ctx
}

func (m *Manager) run(ctx context.Context, id string, req images.Request) (images.Response, error) {
	return m.runWith(ctx, id, req, m.service.Generate)
}

func (m *Manager) runWith(ctx context.Context, id string, req images.Request, generate imageGenerator) (images.Response, error) {
	req.Progress = func(event openaiweb.ProgressEvent) {
		m.update(id, func(task *Task) {
			now := time.Now()
			if event.Progress == "waiting_account" {
				task.Status = StatusQueued
				task.Progress = StatusQueued
				task.ProgressPercent = 0
				task.RealtimeStatus = event.Message
				task.UpdatedAt = now
				appendLog(task, LogEntry{Time: now, Level: "processing", Event: event.Progress, Progress: StatusQueued, Message: event.Message, Details: event.Details})
				return
			}
			if task.Status != StatusRunning {
				task.Status = StatusRunning
				task.StartedAt = &now
				appendLog(task, LogEntry{Time: now, Level: "processing", Event: "started", Progress: "running", Message: "账号已分配，任务开始处理"})
			}
			task.Progress = event.Progress
			task.ProgressPercent = progressPercent(event.Progress)
			task.RealtimeStatus = event.Message
			task.UpdatedAt = now
			appendLog(task, LogEntry{Time: now, Level: "processing", Event: event.Progress, Progress: event.Progress, Message: event.Message, Details: event.Details})
		})
	}
	result, err := generate(ctx, req)

	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.markDirtyLocked(id)
	task := m.tasks[id]
	if task == nil {
		return result, err
	}
	now := time.Now()
	task.FinishedAt = &now
	task.UpdatedAt = now
	delete(m.cancels, id)
	if task.StartedAt != nil {
		task.DurationMS = now.Sub(*task.StartedAt).Milliseconds()
		task.ElapsedSecs = float64(task.DurationMS) / 1000
	}
	if ctx.Err() != nil {
		task.Status = StatusCanceled
		task.Progress = "canceled"
		task.ProgressPercent = 100
		task.RealtimeStatus = "任务已取消"
		task.Error = ctx.Err().Error()
		appendLog(task, LogEntry{Time: now, Level: "warning", Event: "canceled", Progress: "canceled", Message: "任务已取消"})
		return result, ctx.Err()
	}
	if err != nil {
		applyAttemptStats(task, result)
		task.Status = StatusFailed
		task.Progress = "failed"
		task.ProgressPercent = 100
		task.RealtimeStatus = err.Error()
		task.Error = err.Error()
		appendLog(task, LogEntry{Time: now, Level: "error", Event: "failed", Progress: "failed", Message: err.Error()})
		return result, err
	}
	task.Status = StatusSucceeded
	task.Progress = "succeeded"
	task.ProgressPercent = 100
	task.RealtimeStatus = "任务处理完成"
	task.Result = &result
	task.Data = append([]images.Data(nil), result.Data...)
	applyAttemptStats(task, result)
	appendLog(task, LogEntry{Time: now, Level: "success", Event: "completed", Progress: "succeeded", Message: "任务处理完成"})
	return result, nil
}

func applyAttemptStats(task *Task, result images.Response) {
	if task == nil {
		return
	}
	task.ConversationID = result.ConversationID
	task.ImageRouteAttemptCount = len(result.Attempts)
	task.UsedAccountCount = 0
	task.FailedAccountCount = 0
	used := map[string]bool{}
	for _, attempt := range result.Attempts {
		if attempt.AccountEmail != "" {
			used[attempt.AccountEmail] = true
		}
		if attempt.Status == "failed" {
			task.FailedAccountCount++
		}
	}
	task.UsedAccountCount = len(used)
}

func (m *Manager) List(ids []string) []Task {
	return m.ListForOwner(ids, "", true)
}

func (m *Manager) ListForOwner(ids []string, ownerID string, allowAll bool) []Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	filter := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			filter[id] = true
		}
	}
	out := []Task{}
	for id, task := range m.tasks {
		if len(filter) > 0 && !filter[id] {
			continue
		}
		if !allowAll && task.OwnerID != ownerID {
			continue
		}
		out = append(out, m.copyTask(task))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (m *Manager) Status(id string) (Task, bool) {
	return m.StatusForOwner(id, "", true)
}

func (m *Manager) StatusForOwner(id, ownerID string, allowAll bool) (Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	task := m.tasks[id]
	if task == nil || (!allowAll && task.OwnerID != ownerID) {
		return Task{}, false
	}
	return m.copyTask(task), true
}

func (m *Manager) Cancel(id string) (Task, bool) {
	return m.CancelForOwner(id, "", true)
}

func (m *Manager) CancelForOwner(id, ownerID string, allowAll bool) (Task, bool) {
	m.mu.Lock()
	task := m.tasks[id]
	if task == nil || (!allowAll && task.OwnerID != ownerID) {
		m.mu.Unlock()
		return Task{}, false
	}
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return m.StatusForOwner(id, ownerID, allowAll)
}

func (m *Manager) ResumePoll(id string, extraTimeoutSecs float64) (Task, bool) {
	return m.ResumePollForOwner(id, "", true, extraTimeoutSecs)
}

func (m *Manager) ResumePollForOwner(id, ownerID string, allowAll bool, extraTimeoutSecs float64) (Task, bool) {
	if _, ok := m.StatusForOwner(id, ownerID, allowAll); !ok {
		return Task{}, false
	}
	m.update(id, func(task *Task) {
		now := time.Now()
		task.UpdatedAt = now
		task.RealtimeStatus = fmt.Sprintf("收到继续轮询请求：%.0f 秒", extraTimeoutSecs)
		appendLog(task, LogEntry{Time: now, Level: "info", Event: "resume_poll", Progress: task.Progress, Message: task.RealtimeStatus})
	})
	return m.StatusForOwner(id, ownerID, allowAll)
}

func (m *Manager) update(id string, fn func(*Task)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if task := m.tasks[id]; task != nil {
		fn(task)
		m.markDirtyLocked(id)
	}
}

func (m *Manager) load() {
	if m.state == nil {
		return
	}
	var stored []Task
	loadedLegacy := false
	if m.items != nil {
		err := m.items.LoadCollection(context.Background(), taskCollection, &stored)
		if err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return
		}
		if err == nil {
			m.loadTasks(stored)
			return
		}
	}
	if err := m.state.Load(context.Background(), taskCollection, &stored); err != nil {
		if !errors.Is(err, persistence.ErrNotFound) {
			return
		}
		return
	}
	loadedLegacy = true
	m.loadTasks(stored)
	if loadedLegacy && m.items != nil && len(m.tasks) > 0 {
		items := make(map[string]any, len(m.tasks))
		for id, task := range m.tasks {
			items[id] = m.copyTask(task)
		}
		if err := m.items.SaveCollectionItems(context.Background(), taskCollection, items); err == nil {
			_ = m.state.Delete(context.Background(), taskCollection)
			m.dirty = map[string]struct{}{}
		}
	}
}

func (m *Manager) loadTasks(stored []Task) {
	for i := range stored {
		task := stored[i]
		if task.Status == StatusQueued || task.Status == StatusRunning {
			now := time.Now()
			task.Status = StatusFailed
			task.Progress = "failed"
			task.ProgressPercent = 100
			task.RealtimeStatus = "服务重启，未完成任务已终止"
			task.Error = task.RealtimeStatus
			task.FinishedAt = &now
			task.UpdatedAt = now
			appendLog(&task, LogEntry{Time: now, Level: "error", Event: "interrupted", Progress: "failed", Message: task.RealtimeStatus})
			m.dirty[task.ID] = struct{}{}
		}
		m.tasks[task.ID] = &task
	}
}

func (m *Manager) markDirtyLocked(id string) {
	if m.state == nil || id == "" {
		return
	}
	m.dirty[id] = struct{}{}
	m.signalPersistence()
}

func (m *Manager) signalPersistence() {
	if m.wake == nil {
		return
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) persistenceLoop() {
	defer close(m.done)
	for {
		select {
		case <-m.wake:
			timer := time.NewTimer(persistDebounce)
			select {
			case <-timer.C:
				m.persistPending()
			case <-m.stop:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				m.persistPending()
				return
			}
		case <-m.stop:
			m.persistPending()
			return
		}
	}
}

func (m *Manager) persistPending() {
	m.mu.Lock()
	if len(m.dirty) == 0 {
		m.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(m.dirty))
	for id := range m.dirty {
		ids = append(ids, id)
		delete(m.dirty, id)
	}
	collectionItems := make(map[string]any, len(ids))
	for _, id := range ids {
		if task := m.tasks[id]; task != nil {
			collectionItems[id] = m.copyTask(task)
		}
	}
	var document []Task
	if m.items == nil {
		document = make([]Task, 0, len(m.tasks))
		for _, task := range m.tasks {
			document = append(document, m.copyTask(task))
		}
	}
	m.mu.Unlock()

	var err error
	if m.items != nil {
		err = m.items.SaveCollectionItems(context.Background(), taskCollection, collectionItems)
	} else {
		err = m.state.Save(context.Background(), taskCollection, document)
	}
	if err == nil {
		return
	}
	m.mu.Lock()
	for _, id := range ids {
		m.dirty[id] = struct{}{}
	}
	m.mu.Unlock()
	m.signalPersistence()
}

// Close flushes pending task updates without closing the shared persistence
// backend used by the other services.
func (m *Manager) Close() {
	if m == nil || m.stop == nil {
		return
	}
	m.close.Do(func() {
		close(m.stop)
		<-m.done
	})
}

func (m *Manager) copyTask(task *Task) Task {
	if task == nil {
		return Task{}
	}
	cp := *task
	if task.Result != nil {
		result := *task.Result
		result.Data = append([]images.Data(nil), task.Result.Data...)
		result.Attempts = append([]openaiweb.AttemptLog(nil), task.Result.Attempts...)
		cp.Result = &result
	}
	cp.Data = append([]images.Data(nil), task.Data...)
	cp.StatusLogs = append([]LogEntry(nil), task.StatusLogs...)
	return cp
}

func appendLog(task *Task, entry LogEntry) {
	task.StatusLogs = append(task.StatusLogs, entry)
	task.StatusLogCount = len(task.StatusLogs)
}

func progressPercent(progress string) int {
	switch progress {
	case "queued":
		return 0
	case "running":
		return 5
	case "waiting_account":
		return 0
	case "waiting_account_precheck":
		return 10
	case "checking_account":
		return 15
	case "account_validated", "account_ready":
		return 22
	case "account_precheck_failed":
		return 18
	case "retrying_account":
		return 20
	case "uploading":
		return 15
	case "bootstrapping":
		return 25
	case "getting_token":
		return 40
	case "preparing_conversation":
		return 55
	case "starting_generation":
		return 72
	case "image_stream_resolve_start":
		return 88
	case "polling_image":
		return 90
	case "succeeded", "success":
		return 100
	default:
		return 10
	}
}
