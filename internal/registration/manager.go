package registration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/persistence"
)

type Mail struct {
	RequestTimeout int              `json:"request_timeout"`
	WaitTimeout    int              `json:"wait_timeout"`
	WaitInterval   int              `json:"wait_interval"`
	Providers      []map[string]any `json:"providers"`
}

type FlareSolverr struct {
	Enabled      bool   `json:"enabled"`
	URL          string `json:"url"`
	MaxTimeoutMS int    `json:"max_timeout_ms"`
	Preload      bool   `json:"preload"`
}

type Stats struct {
	JobID            string  `json:"job_id,omitempty"`
	Success          int     `json:"success"`
	Fail             int     `json:"fail"`
	Done             int     `json:"done"`
	Running          int     `json:"running"`
	Threads          int     `json:"threads"`
	ElapsedSeconds   float64 `json:"elapsed_seconds"`
	AvgSeconds       float64 `json:"avg_seconds"`
	SuccessRate      float64 `json:"success_rate"`
	CurrentQuota     int     `json:"current_quota"`
	CurrentAvailable int     `json:"current_available"`
	StartedAt        string  `json:"started_at,omitempty"`
	UpdatedAt        string  `json:"updated_at,omitempty"`
	FinishedAt       string  `json:"finished_at,omitempty"`
}

type LogEntry struct {
	Time  string `json:"time"`
	Text  string `json:"text"`
	Level string `json:"level"`
}

type Config struct {
	Enabled         bool         `json:"enabled"`
	Mail            Mail         `json:"mail"`
	Proxy           string       `json:"proxy"`
	FlareSolverr    FlareSolverr `json:"flaresolverr"`
	Total           int          `json:"total"`
	Threads         int          `json:"threads"`
	Mode            string       `json:"mode"`
	TargetQuota     int          `json:"target_quota"`
	TargetAvailable int          `json:"target_available"`
	CheckInterval   int          `json:"check_interval"`
	Stats           Stats        `json:"stats"`
	Logs            []LogEntry   `json:"logs,omitempty"`
}

type Worker func(context.Context, Config, int) (accounts.Account, error)

type logSinkKey struct{}

type logSink func(string, string)

func logStep(ctx context.Context, text, level string) {
	if sink, ok := ctx.Value(logSinkKey{}).(logSink); ok && sink != nil {
		sink(text, level)
	}
}

type Manager struct {
	mu       sync.RWMutex
	path     string
	state    persistence.Store
	accounts *accounts.Store
	worker   Worker
	cfg      Config
	cancel   context.CancelFunc
	running  bool
}

func Default() Config {
	return normalize(Config{
		Mail:         Mail{RequestTimeout: 20, WaitTimeout: 120, WaitInterval: 2, Providers: []map[string]any{{"type": "tempmail_lol", "enabled": true}}},
		FlareSolverr: FlareSolverr{Enabled: true, MaxTimeoutMS: 60000, Preload: true},
		Total:        1, Threads: 1, Mode: "total", TargetQuota: 100, TargetAvailable: 10, CheckInterval: 5,
	})
}

func NewManager(path string, store *accounts.Store, worker Worker) *Manager {
	return newManager(path, nil, store, worker)
}

func NewManagerWithPersistence(state persistence.Store, store *accounts.Store, worker Worker) *Manager {
	return newManager("", state, store, worker)
}

func newManager(path string, state persistence.Store, store *accounts.Store, worker Worker) *Manager {
	m := &Manager{path: path, state: state, accounts: store, worker: worker, cfg: Default()}
	m.load()
	return m
}

func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return clone(m.cfg)
}

func (m *Manager) Update(patch map[string]any) Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, _ := json.Marshal(m.cfg)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	for key, value := range patch {
		data[key] = value
	}
	next, _ := json.Marshal(data)
	_ = json.Unmarshal(next, &m.cfg)
	m.cfg = normalize(m.cfg)
	m.saveLocked()
	return clone(m.cfg)
}

func (m *Manager) Start() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return clone(m.cfg)
	}
	if m.worker == nil {
		m.appendLocked("注册 worker 尚未配置", "red")
		m.saveLocked()
		return clone(m.cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.running, m.cfg.Enabled = cancel, true, true
	m.cfg.Stats = Stats{JobID: fmt.Sprintf("register_%d", time.Now().UnixNano()), Threads: m.cfg.Threads, StartedAt: now(), UpdatedAt: now()}
	m.updateStatsLocked()
	m.appendLocked(fmt.Sprintf("注册任务启动，模式=%s，线程数=%d", m.cfg.Mode, m.cfg.Threads), "yellow")
	m.saveLocked()
	go m.run(ctx)
	return clone(m.cfg)
}

func (m *Manager) Stop() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	m.cfg.Enabled = false
	m.appendLocked("已请求停止注册任务", "yellow")
	m.saveLocked()
	return clone(m.cfg)
}

func (m *Manager) Reset() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Logs = nil
	m.cfg.Stats = Stats{Threads: m.cfg.Threads, CurrentQuota: m.quotaLocked(), CurrentAvailable: m.availableLocked(), UpdatedAt: now()}
	m.saveLocked()
	return clone(m.cfg)
}

func (m *Manager) ResetOutlookPool(scope string) Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendLocked(fmt.Sprintf("Outlook 邮箱池状态重置请求：%s", scope), "yellow")
	m.saveLocked()
	return clone(m.cfg)
}

func (m *Manager) run(ctx context.Context) {
	completed := make(chan struct{}, 128)
	running, submitted := 0, 0
	for {
		cfg := m.Get()
		if ctx.Err() != nil || m.targetReached(cfg, submitted) {
			if running == 0 {
				break
			}
			<-completed
			running--
			continue
		}
		for running < cfg.Threads && !m.targetReached(cfg, submitted) && ctx.Err() == nil {
			submitted++
			running++
			go func(index int) { m.runOne(ctx, index); completed <- struct{}{} }(submitted)
		}
		if running == 0 {
			break
		}
		<-completed
		running--
	}
	m.mu.Lock()
	m.running, m.cancel, m.cfg.Enabled = false, nil, false
	m.cfg.Stats.Running, m.cfg.Stats.FinishedAt = 0, now()
	m.updateStatsLocked()
	m.appendLocked(fmt.Sprintf("注册任务结束，成功%d，失败%d", m.cfg.Stats.Success, m.cfg.Stats.Fail), "yellow")
	m.saveLocked()
	m.mu.Unlock()
}

func (m *Manager) targetReached(cfg Config, submitted int) bool {
	switch cfg.Mode {
	case "quota":
		return m.quotaLocked() >= cfg.TargetQuota
	case "available":
		return m.availableLocked() >= cfg.TargetAvailable
	default:
		return submitted >= cfg.Total
	}
}

func (m *Manager) runOne(ctx context.Context, index int) {
	m.mu.Lock()
	m.cfg.Stats.Running++
	m.updateStatsLocked()
	m.mu.Unlock()
	ctx = context.WithValue(ctx, logSinkKey{}, logSink(m.appendWorkerLog))
	account, err := m.worker(ctx, m.Get(), index)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Stats.Running--
	m.cfg.Stats.Done++
	if err != nil {
		m.cfg.Stats.Fail++
		m.appendLocked(fmt.Sprintf("任务%d 注册失败：%v", index, err), "red")
	} else {
		_, _, _ = m.accounts.AddWithResult([]accounts.Account{account})
		m.cfg.Stats.Success++
		m.appendLocked(fmt.Sprintf("任务%d 注册成功", index), "green")
	}
	m.updateStatsLocked()
	m.saveLocked()
}

func (m *Manager) appendWorkerLog(text, level string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendLocked(text, level)
	m.saveLocked()
}

func (m *Manager) load() {
	if m.state != nil {
		var value Config
		if err := m.state.Load(context.Background(), "registration", &value); err == nil {
			m.cfg = normalize(value)
			m.cfg.Enabled = false
		}
		return
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &m.cfg)
	m.cfg = normalize(m.cfg)
	m.cfg.Enabled = false
}
func (m *Manager) saveLocked() {
	if m.state != nil {
		_ = m.state.Save(context.Background(), "registration", m.cfg)
		return
	}
	if m.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.path), 0755)
	data, _ := json.MarshalIndent(m.cfg, "", "  ")
	_ = os.WriteFile(m.path, append(data, '\n'), 0600)
}
func (m *Manager) appendLocked(text, level string) {
	m.cfg.Logs = append(m.cfg.Logs, LogEntry{Time: now(), Text: text, Level: level})
	if len(m.cfg.Logs) > 300 {
		m.cfg.Logs = m.cfg.Logs[len(m.cfg.Logs)-300:]
	}
}
func (m *Manager) updateStatsLocked() {
	s := &m.cfg.Stats
	s.Threads = m.cfg.Threads
	s.CurrentQuota = m.quotaLocked()
	s.CurrentAvailable = m.availableLocked()
	if t, e := time.Parse(time.RFC3339Nano, s.StartedAt); e == nil {
		s.ElapsedSeconds = round(time.Since(t).Seconds())
		if s.Success > 0 {
			s.AvgSeconds = round(s.ElapsedSeconds / float64(s.Success))
		}
	}
	s.SuccessRate = round(float64(s.Success) * 100 / float64(max(1, s.Success+s.Fail)))
	s.UpdatedAt = now()
}
func (m *Manager) quotaLocked() int {
	n := 0
	if m.accounts == nil {
		return n
	}
	for _, a := range m.accounts.List() {
		if a.Status == "正常" && !a.ImageQuotaUnknown {
			n += a.Quota
		}
	}
	return n
}
func (m *Manager) availableLocked() int {
	n := 0
	if m.accounts == nil {
		return n
	}
	for _, a := range m.accounts.List() {
		if a.Status == "正常" && !a.Disabled {
			n++
		}
	}
	return n
}
func normalize(c Config) Config {
	d := Config{Total: 1, Threads: 1, Mode: "total", TargetQuota: 100, TargetAvailable: 10, CheckInterval: 5, Mail: Mail{RequestTimeout: 20, WaitTimeout: 120, WaitInterval: 2, Providers: []map[string]any{{"type": "tempmail_lol", "enabled": true}}}, FlareSolverr: FlareSolverr{Enabled: true, MaxTimeoutMS: 60000, Preload: true}}
	if c.Total < 1 {
		c.Total = d.Total
	}
	if c.Threads < 1 {
		c.Threads = d.Threads
	}
	if c.Mode != "quota" && c.Mode != "available" {
		c.Mode = "total"
	}
	if c.TargetQuota < 1 {
		c.TargetQuota = d.TargetQuota
	}
	if c.TargetAvailable < 1 {
		c.TargetAvailable = d.TargetAvailable
	}
	if c.CheckInterval < 1 {
		c.CheckInterval = d.CheckInterval
	}
	if c.Mail.RequestTimeout < 1 {
		c.Mail.RequestTimeout = d.Mail.RequestTimeout
	}
	if c.Mail.WaitTimeout < 1 {
		c.Mail.WaitTimeout = d.Mail.WaitTimeout
	}
	if c.Mail.WaitInterval < 1 {
		c.Mail.WaitInterval = d.Mail.WaitInterval
	}
	if c.Mail.Providers == nil {
		c.Mail.Providers = d.Mail.Providers
	}
	if c.FlareSolverr.MaxTimeoutMS < 1000 {
		c.FlareSolverr.MaxTimeoutMS = d.FlareSolverr.MaxTimeoutMS
	}
	c.Stats.Threads = c.Threads
	return c
}
func clone(c Config) Config {
	raw, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(raw, &out)
	return out
}
func now() string             { return time.Now().In(time.Local).Format(time.RFC3339Nano) }
func round(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
