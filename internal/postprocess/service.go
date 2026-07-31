package postprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"imagepool/internal/config"
	"imagepool/internal/persistence"
)

const (
	queueCapacity            = 128
	taskCollection           = "postprocess_tasks"
	completedTaskLoadLimit   = 1000
	completedTaskMemoryLimit = 1000
)

type ProgressFunc func(stage, message string, details map[string]any)

type Options struct {
	ParentTaskID  string
	OwnerID       string
	Model         string
	RequestedSize string
	HDRepair      bool
	Progress      ProgressFunc
}

type Task struct {
	ID              string     `json:"id"`
	ParentTaskID    string     `json:"parent_task_id,omitempty"`
	OwnerID         string     `json:"owner_id,omitempty"`
	Status          string     `json:"status"`
	Model           string     `json:"model,omitempty"`
	RequestedSize   string     `json:"requested_size,omitempty"`
	HDRepair        bool       `json:"hd_repair,omitempty"`
	Restored        bool       `json:"restored,omitempty"`
	Skipped         bool       `json:"skipped,omitempty"`
	InputBytes      int        `json:"input_bytes,omitempty"`
	OutputBytes     int        `json:"output_bytes,omitempty"`
	InputImagePath  string     `json:"input_image_path,omitempty"`
	OutputImagePath string     `json:"output_image_path,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
	DurationMS      int64      `json:"duration_ms,omitempty"`
	Error           string     `json:"error,omitempty"`
}

type HistoryPage struct {
	Items    []Task `json:"items"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Total    int    `json:"total"`
	HasMore  bool   `json:"has_more"`
}

type Result struct {
	Data     []byte
	Restored bool
	Skipped  bool
	Error    string
}

type Stats struct {
	Enabled            bool   `json:"enabled"`
	RestorationEnabled bool   `json:"restoration_enabled"`
	QueueDepth         int    `json:"queue_depth"`
	QueueCapacity      int    `json:"queue_capacity"`
	ActiveWorkers      int    `json:"active_workers"`
	WorkerLimit        int    `json:"worker_limit"`
	WorkerRunning      bool   `json:"worker_running"`
	Processed          uint64 `json:"processed"`
	Failed             uint64 `json:"failed"`
	Restored           uint64 `json:"restored"`
	Skipped            uint64 `json:"skipped"`
	LastError          string `json:"last_error,omitempty"`
}

type workerConfig struct {
	Script             string
	RestorationModel   string
	Timeout            time.Duration
	RestorationEnabled bool
}

type workerRequest struct {
	InputPath        string `json:"input_path"`
	OutputPath       string `json:"output_path"`
	RequestedSize    string `json:"requested_size"`
	Restore          bool   `json:"restore"`
	RestorationModel string `json:"restoration_model"`
}

type workerResponse struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
	Restored bool   `json:"restored"`
	Skipped  bool   `json:"skipped"`
}

type processorRunner interface {
	Process(context.Context, workerConfig, []byte, Options) Result
	Running() bool
	Close()
}

type job struct {
	id      string
	ctx     context.Context
	data    []byte
	options Options
	result  chan Result
}

type Service struct {
	cfgMu sync.RWMutex
	cfg   config.Config

	jobs chan job
	stop chan struct{}
	done chan struct{}
	once sync.Once

	runner  processorRunner
	statsMu sync.RWMutex
	stats   Stats

	taskMu  sync.RWMutex
	taskSeq uint64
	tasks   map[string]*Task
	items   persistence.CollectionStore
}

func New(cfg config.Config, stores ...persistence.Store) *Service {
	var state persistence.Store
	if len(stores) > 0 {
		state = stores[0]
	}
	return newServiceWithState(cfg, &nodeRunner{}, state)
}

func newService(cfg config.Config, runner processorRunner) *Service {
	return newServiceWithState(cfg, runner, nil)
}

func newServiceWithState(cfg config.Config, runner processorRunner, state persistence.Store) *Service {
	service := &Service{
		cfg:    cfg.Normalize(),
		jobs:   make(chan job, queueCapacity),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		runner: runner,
		stats:  Stats{QueueCapacity: queueCapacity, WorkerLimit: 1},
		tasks:  map[string]*Task{},
	}
	if collection, ok := state.(persistence.CollectionStore); ok {
		service.items = collection
		service.loadTasks()
	}
	go service.loop()
	return service
}

func (s *Service) UpdateConfig(cfg config.Config) {
	if s == nil {
		return
	}
	s.cfgMu.Lock()
	s.cfg = cfg.Normalize()
	s.cfgMu.Unlock()
}

func (s *Service) Process(ctx context.Context, data []byte, options Options) Result {
	if s == nil || len(data) == 0 {
		return Result{Data: data, Skipped: true}
	}
	cfg := s.currentWorkerConfig()
	if !(cfg.RestorationEnabled && options.HDRepair) {
		return Result{Data: data, Skipped: true}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Progress != nil {
		options.Progress("postprocess_queued", "图片进入高清处理队列", nil)
	}
	task := s.createTask(options, data, cfg)
	request := job{id: task.ID, ctx: ctx, data: append([]byte(nil), data...), options: options, result: make(chan Result, 1)}
	select {
	case s.jobs <- request:
	case <-ctx.Done():
		s.finishTask(task.ID, Result{Data: data, Error: ctx.Err().Error()})
		return Result{Data: data, Error: ctx.Err().Error()}
	case <-s.stop:
		s.finishTask(task.ID, Result{Data: data, Error: "postprocess service stopped"})
		return Result{Data: data, Error: "postprocess service stopped"}
	}
	select {
	case result := <-request.result:
		return result
	case <-ctx.Done():
		return Result{Data: data, Error: ctx.Err().Error()}
	case <-s.stop:
		return Result{Data: data, Error: "postprocess service stopped"}
	}
}

func (s *Service) loop() {
	defer close(s.done)
	for {
		select {
		case request := <-s.jobs:
			s.run(request)
		case <-s.stop:
			for {
				select {
				case pending := <-s.jobs:
					result := Result{Data: pending.data, Error: "postprocess service stopped"}
					s.finishTask(pending.id, result)
					pending.result <- result
				default:
					return
				}
			}
		}
	}
}

func (s *Service) run(request job) {
	cfg := s.currentWorkerConfig()
	s.startTask(request.id)
	s.statsMu.Lock()
	s.stats.ActiveWorkers = 1
	s.statsMu.Unlock()
	defer func() {
		s.statsMu.Lock()
		s.stats.ActiveWorkers = 0
		s.statsMu.Unlock()
	}()

	ctx := request.ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()
	result := s.runner.Process(ctx, cfg, request.data, request.options)
	if len(result.Data) == 0 {
		result.Data = request.data
	}
	if request.options.Progress != nil {
		request.options.Progress("postprocess_complete", "图片高清处理完成", map[string]any{
			"restored": result.Restored,
			"fallback": result.Error != "",
		})
	}
	s.statsMu.Lock()
	s.stats.Processed++
	if result.Error != "" {
		s.stats.Failed++
		s.stats.LastError = result.Error
	}
	if result.Restored {
		s.stats.Restored++
	}
	if result.Skipped {
		s.stats.Skipped++
	}
	s.statsMu.Unlock()
	s.finishTask(request.id, result)
	request.result <- result
}

func (s *Service) createTask(options Options, data []byte, cfg workerConfig) Task {
	now := time.Now()
	s.taskMu.Lock()
	s.taskSeq++
	id := fmt.Sprintf("pp_%d_%d", now.UnixNano(), s.taskSeq)
	s.taskMu.Unlock()
	inputImagePath := s.saveComparisonImage(id, "before", data)
	s.taskMu.Lock()
	task := &Task{
		ID:             id,
		ParentTaskID:   strings.TrimSpace(options.ParentTaskID),
		OwnerID:        strings.TrimSpace(options.OwnerID),
		Status:         "queued",
		Model:          strings.TrimSpace(options.Model),
		RequestedSize:  strings.TrimSpace(options.RequestedSize),
		HDRepair:       cfg.RestorationEnabled && options.HDRepair,
		InputBytes:     len(data),
		InputImagePath: inputImagePath,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.tasks[task.ID] = task
	snapshot := *task
	s.taskMu.Unlock()
	s.persistTask(snapshot)
	return snapshot
}

func (s *Service) startTask(id string) {
	now := time.Now()
	s.taskMu.Lock()
	task := s.tasks[id]
	if task == nil {
		s.taskMu.Unlock()
		return
	}
	task.Status = "running"
	task.StartedAt = &now
	task.UpdatedAt = now
	snapshot := *task
	s.taskMu.Unlock()
	s.persistTask(snapshot)
}

func (s *Service) finishTask(id string, result Result) {
	now := time.Now()
	outputImagePath := ""
	inputImageToRemove := ""
	if result.Error == "" && result.Restored {
		outputImagePath = s.saveComparisonImage(id, "after", result.Data)
	}
	s.taskMu.Lock()
	task := s.tasks[id]
	if task == nil {
		s.taskMu.Unlock()
		return
	}
	task.Restored = result.Restored
	task.Skipped = result.Skipped
	task.OutputBytes = len(result.Data)
	task.OutputImagePath = outputImagePath
	task.Error = strings.TrimSpace(result.Error)
	switch {
	case task.Error != "":
		task.Status = "error"
	case task.Restored:
		task.Status = "success"
	default:
		task.Status = "skipped"
		task.Skipped = true
	}
	task.FinishedAt = &now
	task.UpdatedAt = now
	task.DurationMS = now.Sub(task.CreatedAt).Milliseconds()
	if task.Status != "success" {
		inputImageToRemove = task.InputImagePath
		task.InputImagePath = ""
	}
	snapshot := *task
	s.pruneTaskMemoryLocked()
	s.taskMu.Unlock()
	s.removeComparisonImage(inputImageToRemove)
	s.persistTask(snapshot)
}

func (s *Service) saveComparisonImage(taskID, side string, data []byte) string {
	extension := comparisonImageExtension(data)
	if extension == "" || strings.TrimSpace(taskID) == "" {
		return ""
	}
	s.cfgMu.RLock()
	root := filepath.Clean(s.cfg.ImageOutputDir)
	s.cfgMu.RUnlock()
	rel := filepath.ToSlash(filepath.Join(".postprocess-comparisons", taskID, side+extension))
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("create postprocess comparison directory: %v", err)
		return ""
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("write postprocess comparison image: %v", err)
		return ""
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("save postprocess comparison image: %v", err)
		return ""
	}
	return rel
}

func (s *Service) removeComparisonImage(rel string) {
	if !strings.HasPrefix(filepath.ToSlash(rel), ".postprocess-comparisons/") {
		return
	}
	s.cfgMu.RLock()
	root := filepath.Clean(s.cfg.ImageOutputDir)
	s.cfgMu.RUnlock()
	_ = os.Remove(filepath.Join(root, filepath.FromSlash(rel)))
}

func comparisonImageExtension(data []byte) string {
	switch http.DetectContentType(data) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func (s *Service) persistTask(task Task) {
	if s == nil || s.items == nil || strings.TrimSpace(task.ID) == "" {
		return
	}
	if err := s.items.SaveCollectionItems(context.Background(), taskCollection, map[string]any{task.ID: task}); err != nil {
		log.Printf("persist postprocess task %s: %v", task.ID, err)
	}
}

func (s *Service) loadTasks() {
	if s == nil || s.items == nil {
		return
	}
	var stored []Task
	var err error
	if window, ok := s.items.(persistence.CollectionWindowStore); ok {
		err = window.LoadCollectionWindow(context.Background(), taskCollection, persistence.CollectionWindow{
			UpdatedSince:   time.Now().Add(-30 * 24 * time.Hour),
			CompletedLimit: completedTaskLoadLimit,
			ActiveStatuses: []string{"queued", "running"},
		}, &stored)
	} else {
		err = s.items.LoadCollection(context.Background(), taskCollection, &stored)
	}
	if err != nil {
		if !errors.Is(err, persistence.ErrNotFound) {
			log.Printf("load postprocess tasks: %v", err)
		}
		return
	}
	now := time.Now()
	interrupted := make([]Task, 0)
	s.taskMu.Lock()
	for i := range stored {
		task := stored[i]
		if strings.TrimSpace(task.ID) == "" {
			continue
		}
		if task.Status == "queued" || task.Status == "running" {
			task.Status = "error"
			task.Error = "服务重启，高清处理任务已终止"
			task.FinishedAt = &now
			task.UpdatedAt = now
			task.DurationMS = now.Sub(task.CreatedAt).Milliseconds()
			interrupted = append(interrupted, task)
		}
		copy := task
		s.tasks[task.ID] = &copy
	}
	s.pruneTaskMemoryLocked()
	s.taskMu.Unlock()
	for _, task := range interrupted {
		s.persistTask(task)
	}
}

func (s *Service) pruneTaskMemoryLocked() {
	if len(s.tasks) <= completedTaskMemoryLimit {
		return
	}
	completed := make([]*Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		if task != nil && task.Status != "queued" && task.Status != "running" {
			completed = append(completed, task)
		}
	}
	sort.Slice(completed, func(i, j int) bool { return completed[i].UpdatedAt.Before(completed[j].UpdatedAt) })
	for len(s.tasks) > completedTaskMemoryLimit && len(completed) > 0 {
		delete(s.tasks, completed[0].ID)
		completed = completed[1:]
	}
}

func (s *Service) History(page, pageSize int, ownerID string, allowAll bool) (HistoryPage, error) {
	page, pageSize = normalizeHistoryPage(page, pageSize)
	result := HistoryPage{Page: page, PageSize: pageSize}
	offset := (page - 1) * pageSize
	if pager, ok := s.items.(persistence.CollectionPageStore); ok {
		var stored []Task
		total, err := pager.LoadCollectionPage(context.Background(), taskCollection, persistence.CollectionPage{
			Limit: pageSize, Offset: offset, OwnerID: strings.TrimSpace(ownerID), AllowAll: allowAll,
		}, &stored)
		if err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return result, err
		}
		result.Items = stored
		result.Total = total
		result.HasMore = offset+len(stored) < total
		return result, nil
	}
	s.taskMu.RLock()
	items := make([]Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		if task == nil || (!allowAll && task.OwnerID != ownerID) {
			continue
		}
		items = append(items, *task)
	}
	s.taskMu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	result.Total = len(items)
	if offset >= len(items) {
		result.Items = []Task{}
		return result, nil
	}
	end := min(offset+pageSize, len(items))
	result.Items = append([]Task(nil), items[offset:end]...)
	result.HasMore = end < len(items)
	return result, nil
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

func (s *Service) currentWorkerConfig() workerConfig {
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	return workerConfig{
		Script:             strings.TrimSpace(cfg.ImagePostprocessWorker),
		RestorationModel:   strings.TrimSpace(cfg.ImageRestorationModel),
		Timeout:            time.Duration(cfg.ImagePostprocessTimeoutSecs * float64(time.Second)),
		RestorationEnabled: cfg.ImageRestorationEnabled,
	}
}

func (s *Service) Stats() Stats {
	if s == nil {
		return Stats{QueueCapacity: queueCapacity, WorkerLimit: 1}
	}
	cfg := s.currentWorkerConfig()
	s.statsMu.RLock()
	stats := s.stats
	s.statsMu.RUnlock()
	stats.Enabled = cfg.RestorationEnabled
	stats.RestorationEnabled = cfg.RestorationEnabled
	stats.QueueDepth = len(s.jobs)
	stats.QueueCapacity = cap(s.jobs)
	stats.WorkerLimit = 1
	stats.WorkerRunning = s.runner != nil && s.runner.Running()
	return stats
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.stop)
		if s.runner != nil {
			s.runner.Close()
		}
		<-s.done
	})
}

type nodeRunner struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	seq    uint64
}

func (r *nodeRunner) Process(ctx context.Context, cfg workerConfig, data []byte, options Options) Result {
	tempDir, err := os.MkdirTemp("", "image-pool-postprocess-")
	if err != nil {
		return Result{Data: data, Error: err.Error()}
	}
	defer os.RemoveAll(tempDir)
	inputPath := filepath.Join(tempDir, "input.image")
	outputPath := filepath.Join(tempDir, "output.png")
	if err := os.WriteFile(inputPath, data, 0o600); err != nil {
		return Result{Data: data, Error: err.Error()}
	}
	request := workerRequest{
		InputPath: inputPath, OutputPath: outputPath, RequestedSize: options.RequestedSize,
		Restore: cfg.RestorationEnabled && options.HDRepair, RestorationModel: cfg.RestorationModel,
	}
	if request.Restore && options.Progress != nil {
		options.Progress("restoring_image", "正在进行高清修复", nil)
	}
	response, err := r.call(ctx, cfg.Script, request)
	if err != nil || !response.OK {
		if err == nil {
			err = errors.New(response.Error)
		}
		return Result{Data: data, Error: err.Error()}
	}
	processed, err := os.ReadFile(outputPath)
	if err != nil {
		return Result{Data: data, Error: err.Error()}
	}
	return Result{Data: processed, Restored: response.Restored, Skipped: response.Skipped}
}

func (r *nodeRunner) call(ctx context.Context, script string, request workerRequest) (workerResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureStarted(script); err != nil {
		return workerResponse{}, err
	}
	r.seq++
	payload := struct {
		ID uint64 `json:"id"`
		workerRequest
	}{ID: r.seq, workerRequest: request}
	line, err := json.Marshal(payload)
	if err != nil {
		return workerResponse{}, err
	}
	stdin := r.stdin
	stdout := r.stdout
	responseCh := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		if _, writeErr := stdin.Write(append(line, '\n')); writeErr != nil {
			responseCh <- struct {
				line string
				err  error
			}{err: writeErr}
			return
		}
		responseLine, readErr := stdout.ReadString('\n')
		responseCh <- struct {
			line string
			err  error
		}{line: responseLine, err: readErr}
	}()
	select {
	case received := <-responseCh:
		if received.err != nil {
			r.stopLocked()
			return workerResponse{}, received.err
		}
		var envelope struct {
			ID uint64 `json:"id"`
			workerResponse
		}
		if err := json.Unmarshal([]byte(received.line), &envelope); err != nil {
			return workerResponse{}, fmt.Errorf("decode postprocess worker response: %w", err)
		}
		if envelope.ID != r.seq {
			return workerResponse{}, fmt.Errorf("postprocess worker response id mismatch")
		}
		return envelope.workerResponse, nil
	case <-ctx.Done():
		r.stopLocked()
		return workerResponse{}, ctx.Err()
	}
}

func (r *nodeRunner) ensureStarted(script string) error {
	if r.cmd != nil && r.cmd.Process != nil {
		return nil
	}
	if strings.TrimSpace(script) == "" {
		return fmt.Errorf("image postprocess worker path is empty")
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("image postprocess worker: %w", err)
	}
	cmd := exec.Command("node", script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd = cmd
	r.stdin = stdin
	r.stdout = bufio.NewReader(stdout)
	return nil
}

func (r *nodeRunner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd != nil && r.cmd.Process != nil
}

func (r *nodeRunner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
}

func (r *nodeRunner) stopLocked() {
	if r.stdin != nil {
		_ = r.stdin.Close()
	}
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
		_ = r.cmd.Wait()
	}
	r.cmd = nil
	r.stdin = nil
	r.stdout = nil
}
