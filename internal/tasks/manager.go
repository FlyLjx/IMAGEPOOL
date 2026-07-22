package tasks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"imagepool/internal/errorinfo"
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

	// Keep async task admission bounded. Account leases remain the effective
	// upstream concurrency limit; this prevents a traffic burst from creating
	// an unbounded number of waiting goroutines before it reaches that pool.
	asyncTaskWorkerLimit = 128
	asyncTaskQueueLimit  = 4096
	maxTaskStatusLogs    = 200

	completedTaskMemoryTTL    = 30 * time.Minute
	maxCompletedInMemoryTasks = 500
	maxCompletedStatusLogs    = 50
	maxCompletedPromptRunes   = 800
)

type ImageService interface {
	Generate(ctx context.Context, req images.Request) (images.Response, error)
	GenerateWithAccount(ctx context.Context, token string, req images.Request) (images.Response, error)
}

type imageGenerator func(context.Context, images.Request) (images.Response, error)

type queuedTask struct {
	id  string
	ctx context.Context
	req images.Request
}

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
	ResponseFormat         string           `json:"response_format,omitempty"`
	OutputFormat           string           `json:"output_format,omitempty"`
	CreatedAt              time.Time        `json:"created_at"`
	StartedAt              *time.Time       `json:"started_at,omitempty"`
	FinishedAt             *time.Time       `json:"finished_at,omitempty"`
	UpdatedAt              time.Time        `json:"updated_at"`
	Result                 *images.Response `json:"-"`
	Data                   []images.Data    `json:"data,omitempty"`
	ConversationID         string           `json:"conversation_id,omitempty"`
	UsedAccountCount       int              `json:"used_account_count,omitempty"`
	FailedAccountCount     int              `json:"failed_account_count,omitempty"`
	ImageRouteAttemptCount int              `json:"image_route_attempt_count,omitempty"`
	DurationMS             int64            `json:"duration_ms,omitempty"`
	ElapsedSecs            float64          `json:"elapsed_secs,omitempty"`
	Error                  string           `json:"error,omitempty"`
	ErrorCode              string           `json:"error_code,omitempty"`
	ErrorTitle             string           `json:"error_title,omitempty"`
	ErrorCategory          string           `json:"error_category,omitempty"`
	ErrorCategoryLabel     string           `json:"error_category_label,omitempty"`
	ErrorRetryable         bool             `json:"error_retryable,omitempty"`
	ErrorAction            string           `json:"error_action,omitempty"`
	ErrorHint              string           `json:"error_hint,omitempty"`
	StatusLogCount         int              `json:"status_log_count"`
	StatusLogs             []LogEntry       `json:"status_logs,omitempty"`
}

type HistoryPage struct {
	Items    []Task `json:"items"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Total    int    `json:"total"`
	HasMore  bool   `json:"has_more"`
}

type Manager struct {
	mu              sync.RWMutex
	seq             uint64
	service         ImageService
	state           persistence.Store
	items           persistence.CollectionStore
	tasks           map[string]*Task
	cancels         map[string]context.CancelFunc
	dirty           map[string]struct{}
	persisted       map[string]bool
	wake            chan struct{}
	stop            chan struct{}
	done            chan struct{}
	dispatchMu      sync.Mutex
	dispatchClosing bool
	jobs            chan queuedTask
	dispatchStop    chan struct{}
	dispatchDone    chan struct{}
	workerSlots     chan struct{}
	workers         sync.WaitGroup
	close           sync.Once
}

func NewManager(service ImageService) *Manager {
	return newManager(service, nil)
}

func NewManagerWithPersistence(service ImageService, state persistence.Store) *Manager {
	return newManager(service, state)
}

func newManager(service ImageService, state persistence.Store) *Manager {
	m := &Manager{
		service:      service,
		state:        state,
		tasks:        map[string]*Task{},
		cancels:      map[string]context.CancelFunc{},
		dirty:        map[string]struct{}{},
		persisted:    map[string]bool{},
		jobs:         make(chan queuedTask, asyncTaskQueueLimit),
		dispatchStop: make(chan struct{}),
		dispatchDone: make(chan struct{}),
		workerSlots:  make(chan struct{}, asyncTaskWorkerLimit),
	}
	if collection, ok := state.(persistence.CollectionStore); ok {
		m.items = collection
	}
	if state != nil {
		m.wake = make(chan struct{}, 1)
		m.stop = make(chan struct{})
		m.done = make(chan struct{})
	}
	m.load()
	go m.dispatchLoop()
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
	if !m.enqueue(task.ID, ctx, req) {
		if rejected, ok := m.Status(task.ID); ok {
			return rejected
		}
	}
	return task
}

func (m *Manager) enqueue(id string, ctx context.Context, req images.Request) bool {
	if m == nil {
		return false
	}
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	if m.dispatchClosing {
		m.rejectQueuedTask(id, "服务正在停止，任务未执行")
		return false
	}
	select {
	case m.jobs <- queuedTask{id: id, ctx: ctx, req: req}:
		return true
	default:
		m.rejectQueuedTask(id, "任务队列已满，请稍后重试")
		return false
	}
}

func (m *Manager) dispatchLoop() {
	defer close(m.dispatchDone)
	for {
		select {
		case <-m.dispatchStop:
			m.rejectPendingTasks("服务停止，任务未执行")
			return
		case job := <-m.jobs:
			select {
			case <-m.dispatchStop:
				m.rejectQueuedTask(job.id, "服务停止，任务未执行")
				m.rejectPendingTasks("服务停止，任务未执行")
				return
			case m.workerSlots <- struct{}{}:
			}
			m.workers.Add(1)
			go func() {
				defer func() {
					<-m.workerSlots
					m.workers.Done()
				}()
				_, _ = m.run(job.ctx, job.id, job.req)
			}()
		}
	}
}

func (m *Manager) rejectPendingTasks(message string) {
	for {
		select {
		case job := <-m.jobs:
			m.rejectQueuedTask(job.id, message)
		default:
			return
		}
	}
}

func (m *Manager) rejectQueuedTask(id, message string) {
	var cancel context.CancelFunc
	m.mu.Lock()
	if task := m.tasks[id]; task != nil && task.Status == StatusQueued {
		now := time.Now()
		classified := errorinfo.ClassifyText(message, 0)
		task.Status = StatusFailed
		task.Progress = "failed"
		task.ProgressPercent = 100
		applyTaskError(task, classified)
		task.FinishedAt = &now
		task.UpdatedAt = now
		appendLog(task, LogEntry{Time: now, Level: "error", Event: "rejected", Progress: "failed", Message: classified.Message})
		compactCompletedTask(task)
		m.markDirtyLocked(id)
		m.pruneCompletedLocked(now)
	}
	cancel = m.cancels[id]
	delete(m.cancels, id)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	task := &Task{ID: id, OwnerID: strings.TrimSpace(ownerID), ClientTaskID: clientTaskID, Mode: mode, Status: StatusQueued, Progress: "queued", ProgressPercent: 0, RealtimeStatus: "任务已提交", Prompt: req.Prompt, Model: images.PublicImageModel, Size: req.Size, Quality: req.Quality, ResponseFormat: req.ResponseFormat, OutputFormat: req.OutputFormat, CreatedAt: now, UpdatedAt: now}
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
		event = openaiweb.PublicProgressEvent(event)
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
				appendLog(task, LogEntry{Time: now, Level: "processing", Event: "started", Progress: "running", Message: "任务开始处理"})
			}
			task.Progress = event.Progress
			task.ProgressPercent = progressPercent(event.Progress)
			task.RealtimeStatus = event.Message
			task.UpdatedAt = now
			appendLog(task, LogEntry{Time: now, Level: "processing", Event: event.Progress, Progress: event.Progress, Message: event.Message, Details: event.Details})
		})
	}
	result, err := generate(ctx, req)
	internalResult := result
	result = publicImageResponse(result)

	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[id]
	if task == nil {
		return result, err
	}
	defer func() {
		compactCompletedTask(task)
		m.markDirtyLocked(id)
		m.pruneCompletedLocked(time.Now())
	}()
	now := time.Now()
	task.FinishedAt = &now
	task.UpdatedAt = now
	delete(m.cancels, id)
	if task.StartedAt != nil {
		task.DurationMS = now.Sub(*task.StartedAt).Milliseconds()
		task.ElapsedSecs = float64(task.DurationMS) / 1000
	}
	// A task deadline is a generation timeout, not a user cancellation. Keep
	// the public timeout status/error while reserving "任务已取消" for an
	// explicit client cancellation or service shutdown.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w: task deadline exceeded", openaiweb.ErrPollTimeout)
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		classified := errorinfo.Classify(ctx.Err(), 0)
		task.Status = StatusCanceled
		task.Progress = "canceled"
		task.ProgressPercent = 100
		applyTaskError(task, classified)
		appendLog(task, LogEntry{Time: now, Level: "warning", Event: "canceled", Progress: "canceled", Message: classified.Message})
		return result, ctx.Err()
	}
	if err != nil {
		classified := errorinfo.Classify(err, 0)
		applyAttemptStats(task, internalResult)
		task.Status = StatusFailed
		task.Progress = "failed"
		task.ProgressPercent = 100
		applyTaskError(task, classified)
		appendLog(task, LogEntry{Time: now, Level: "error", Event: "failed", Progress: "failed", Message: classified.Message})
		return result, err
	}
	task.Status = StatusSucceeded
	task.Progress = "succeeded"
	task.ProgressPercent = 100
	task.RealtimeStatus = "任务处理完成"
	task.Result = &result
	task.Data = append([]images.Data(nil), result.Data...)
	applyAttemptStats(task, internalResult)
	appendLog(task, LogEntry{Time: now, Level: "success", Event: "completed", Progress: "succeeded", Message: "任务处理完成"})
	return result, nil
}

func applyTaskError(task *Task, classified errorinfo.Info) {
	if task == nil {
		return
	}
	task.RealtimeStatus = classified.Message
	task.Error = classified.Message
	task.ErrorCode = classified.Code
	task.ErrorTitle = classified.Title
	task.ErrorCategory = classified.Category
	task.ErrorCategoryLabel = errorinfo.CategoryLabel(classified.Category)
	task.ErrorRetryable = classified.Retryable
	task.ErrorAction = classified.Action
	task.ErrorHint = classified.Hint
}

// publicImageResponse is the task-manager output boundary. Image services keep
// original errors for account handling; task responses and persisted records
// only retain their safe attempt projection.
func publicImageResponse(result images.Response) images.Response {
	return result.Public()
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (m *Manager) HistoryForOwner(page, pageSize int, ownerID string, allowAll bool) (HistoryPage, error) {
	page, pageSize = normalizeHistoryPage(page, pageSize)
	result := HistoryPage{Page: page, PageSize: pageSize}
	offset := (page - 1) * pageSize
	if pager, ok := m.items.(persistence.CollectionPageStore); ok {
		var stored []Task
		total, err := pager.LoadCollectionPage(context.Background(), taskCollection, persistence.CollectionPage{Limit: pageSize, Offset: offset, OwnerID: ownerID, AllowAll: allowAll}, &stored)
		if err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return result, err
		}
		result.Total = total
		result.Items = make([]Task, 0, len(stored))
		for i := range stored {
			task := stored[i]
			if !allowAll && task.OwnerID != ownerID {
				continue
			}
			compactCompletedTask(&task)
			result.Items = append(result.Items, m.copyTask(&task))
		}
		result.HasMore = offset+len(result.Items) < result.Total
		return result, nil
	}
	items := m.ListForOwner(nil, ownerID, allowAll)
	result.Total = len(items)
	if offset >= len(items) {
		result.Items = []Task{}
		return result, nil
	}
	end := offset + pageSize
	if end > len(items) {
		end = len(items)
	}
	result.Items = append([]Task(nil), items[offset:end]...)
	result.HasMore = end < len(items)
	return result, nil
}

func (m *Manager) HistoryTotalForOwner(ownerID string, allowAll bool) (int, error) {
	if m == nil {
		return 0, nil
	}
	if pager, ok := m.items.(persistence.CollectionPageStore); ok {
		var stored []Task
		total, err := pager.LoadCollectionPage(context.Background(), taskCollection, persistence.CollectionPage{Limit: 1, OwnerID: ownerID, AllowAll: allowAll}, &stored)
		if err != nil {
			if !errors.Is(err, persistence.ErrNotFound) {
				return len(m.ListForOwner(nil, ownerID, allowAll)), err
			}
			total = 0
		}
		m.mu.RLock()
		for id, task := range m.tasks {
			if task == nil {
				continue
			}
			if !allowAll && task.OwnerID != ownerID {
				continue
			}
			if !m.persisted[id] {
				total++
			}
		}
		m.mu.RUnlock()
		return total, nil
	}
	return len(m.ListForOwner(nil, ownerID, allowAll)), nil
}

func normalizeHistoryPage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

func (m *Manager) Status(id string) (Task, bool) {
	return m.StatusForOwner(id, "", true)
}

func (m *Manager) StatusForOwner(id, ownerID string, allowAll bool) (Task, bool) {
	m.mu.RLock()
	task := m.tasks[id]
	if task != nil && (allowAll || task.OwnerID == ownerID) {
		snapshot := m.copyTask(task)
		m.mu.RUnlock()
		return snapshot, true
	}
	m.mu.RUnlock()

	if id == "" {
		return Task{}, false
	}
	itemStore, ok := m.items.(persistence.CollectionItemStore)
	if !ok {
		return Task{}, false
	}
	var stored Task
	if err := itemStore.LoadCollectionItem(context.Background(), taskCollection, id, &stored); err != nil {
		return Task{}, false
	}
	if !allowAll && stored.OwnerID != ownerID {
		return Task{}, false
	}
	compactCompletedTask(&stored)
	return m.copyTask(&stored), true
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
		var err error
		if window, ok := m.items.(persistence.CollectionWindowStore); ok {
			err = window.LoadCollectionWindow(context.Background(), taskCollection, persistence.CollectionWindow{
				UpdatedSince:   time.Now().Add(-completedTaskMemoryTTL),
				CompletedLimit: maxCompletedInMemoryTasks,
				ActiveStatuses: []string{StatusQueued, StatusRunning},
			}, &stored)
		} else {
			err = m.items.LoadCollection(context.Background(), taskCollection, &stored)
		}
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
			for id := range items {
				m.persisted[id] = true
			}
			m.dirty = map[string]struct{}{}
		}
	}
}

func (m *Manager) loadTasks(stored []Task) {
	now := time.Now()
	for i := range stored {
		task := stored[i]
		if task.Status == StatusQueued || task.Status == StatusRunning {
			classified := errorinfo.ClassifyText("服务重启，未完成任务已终止", 0)
			task.Status = StatusFailed
			task.Progress = "failed"
			task.ProgressPercent = 100
			applyTaskError(&task, classified)
			task.FinishedAt = &now
			task.UpdatedAt = now
			appendLog(&task, LogEntry{Time: now, Level: "error", Event: "interrupted", Progress: "failed", Message: classified.Message})
			m.dirty[task.ID] = struct{}{}
		}
		if compactCompletedTask(&task) {
			m.dirty[task.ID] = struct{}{}
		}
		m.tasks[task.ID] = &task
		m.persisted[task.ID] = true
	}
	m.pruneCompletedLocked(now)
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
		m.mu.Lock()
		if m.persisted == nil {
			m.persisted = map[string]bool{}
		}
		if m.items != nil {
			for id := range collectionItems {
				m.persisted[id] = true
			}
		} else {
			for _, task := range document {
				m.persisted[task.ID] = true
			}
		}
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	for _, id := range ids {
		m.dirty[id] = struct{}{}
	}
	m.mu.Unlock()
	m.signalPersistence()
}

// Close stops task admission, cancels active work, and flushes task state
// without closing the shared persistence backend used by other services.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.close.Do(func() {
		m.dispatchMu.Lock()
		m.dispatchClosing = true
		if m.dispatchStop != nil {
			close(m.dispatchStop)
		}
		m.dispatchMu.Unlock()

		m.cancelAll()
		if m.dispatchDone != nil {
			<-m.dispatchDone
		}
		m.workers.Wait()

		if m.stop != nil {
			close(m.stop)
			<-m.done
		}
	})
}

func (m *Manager) cancelAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.cancels))
	for _, cancel := range m.cancels {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (m *Manager) copyTask(task *Task) Task {
	if task == nil {
		return Task{}
	}
	cp := *task
	cp.Model = images.PublicImageModel
	cp.Error = openaiweb.PublicErrorText(cp.Error)
	cp.RealtimeStatus = openaiweb.PublicErrorText(cp.RealtimeStatus)
	if task.Result != nil {
		result := task.Result.Public()
		result.Data = append([]images.Data(nil), task.Result.Data...)
		cp.Result = &result
	}
	cp.Data = append([]images.Data(nil), task.Data...)
	cp.StatusLogs = make([]LogEntry, len(task.StatusLogs))
	copy(cp.StatusLogs, task.StatusLogs)
	for i := range cp.StatusLogs {
		cp.StatusLogs[i].Message = openaiweb.PublicErrorText(cp.StatusLogs[i].Message)
		cp.StatusLogs[i].Details = openaiweb.PublicDetails(cp.StatusLogs[i].Details)
	}
	return cp
}

func appendLog(task *Task, entry LogEntry) {
	entry.Message = openaiweb.PublicErrorText(entry.Message)
	entry.Details = openaiweb.PublicDetails(entry.Details)
	if task.StatusLogCount < len(task.StatusLogs) {
		task.StatusLogCount = len(task.StatusLogs)
	}
	if len(task.StatusLogs) >= maxTaskStatusLogs {
		copy(task.StatusLogs, task.StatusLogs[len(task.StatusLogs)-maxTaskStatusLogs+1:])
		task.StatusLogs = task.StatusLogs[:maxTaskStatusLogs-1]
	}
	task.StatusLogs = append(task.StatusLogs, entry)
	task.StatusLogCount++
}

func compactCompletedTask(task *Task) bool {
	if !isCompletedTask(task) {
		return false
	}
	changed := false
	if task.Result != nil {
		task.Result = nil
		changed = true
	}
	if task.StatusLogCount < len(task.StatusLogs) {
		task.StatusLogCount = len(task.StatusLogs)
		changed = true
	}
	if len(task.StatusLogs) > maxCompletedStatusLogs {
		task.StatusLogs = append([]LogEntry(nil), task.StatusLogs[len(task.StatusLogs)-maxCompletedStatusLogs:]...)
		changed = true
	}
	if trimmed := truncateRunes(task.Prompt, maxCompletedPromptRunes); trimmed != task.Prompt {
		task.Prompt = trimmed
		changed = true
	}
	return changed
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

func isCompletedTask(task *Task) bool {
	if task == nil {
		return false
	}
	if task.Status == StatusQueued || task.Status == StatusRunning {
		return false
	}
	return task.FinishedAt != nil || task.Status == StatusSucceeded || task.Status == StatusFailed
}

func taskCompletionTime(task *Task) time.Time {
	if task == nil {
		return time.Time{}
	}
	if task.FinishedAt != nil && !task.FinishedAt.IsZero() {
		return *task.FinishedAt
	}
	if !task.UpdatedAt.IsZero() {
		return task.UpdatedAt
	}
	return task.CreatedAt
}

func (m *Manager) pruneCompletedLocked(now time.Time) []string {
	// Collection stores such as PostgreSQL persist each task independently, so
	// completed tasks can be evicted from memory without deleting history. The
	// legacy JSON document store rewrites the whole in-memory task list on each
	// save; pruning there would erase persisted history, so keep JSON tasks in
	// memory unless/until that backend gets an append-only history store.
	if m.state != nil && m.items == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	type candidate struct {
		id string
		at time.Time
	}
	candidates := make([]candidate, 0)
	prune := map[string]bool{}
	for id, task := range m.tasks {
		if !isCompletedTask(task) {
			continue
		}
		at := taskCompletionTime(task)
		candidates = append(candidates, candidate{id: id, at: at})
		if !at.IsZero() && now.Sub(at) > completedTaskMemoryTTL {
			prune[id] = true
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	remainingCompleted := len(candidates) - len(prune)
	if remainingCompleted > maxCompletedInMemoryTasks {
		sort.Slice(candidates, func(i, j int) bool {
			a := candidates[i]
			b := candidates[j]
			if a.at.Equal(b.at) {
				return a.id < b.id
			}
			if a.at.IsZero() {
				return true
			}
			if b.at.IsZero() {
				return false
			}
			return a.at.Before(b.at)
		})
		for _, item := range candidates {
			if remainingCompleted <= maxCompletedInMemoryTasks {
				break
			}
			if prune[item.id] {
				continue
			}
			prune[item.id] = true
			remainingCompleted--
		}
	}
	if len(prune) == 0 {
		return nil
	}
	ids := make([]string, 0, len(prune))
	for id := range prune {
		delete(m.tasks, id)
		delete(m.cancels, id)
		delete(m.persisted, id)
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
