package httpapi

import (
	"context"
	"fmt"
	"math"
	"time"

	"imagepool/internal/config"
)

// imagePoolAutoRegistrationStatus is intentionally returned with the capacity
// response. It makes the automatic decision visible in the admin dashboard and
// avoids having to infer registration activity from the manual registration
// panel.
type imagePoolAutoRegistrationStatus struct {
	Enabled                           bool       `json:"enabled"`
	Running                           bool       `json:"running"`
	Automatic                         bool       `json:"automatic"`
	Status                            string     `json:"status"`
	Reason                            string     `json:"reason"`
	PendingTasks                      int        `json:"pending_tasks"`
	IdleForSecs                       float64    `json:"idle_for_secs"`
	CurrentUsableAccounts             int        `json:"current_usable_accounts"`
	CurrentDispatchableAccounts       int        `json:"current_dispatchable_accounts"`
	InflightRegistrations             int        `json:"inflight_registrations"`
	TargetUsableAccounts              int        `json:"target_usable_accounts"`
	BatchTargetUsableAccounts         int        `json:"batch_target_usable_accounts"`
	NeedUsableAccounts                int        `json:"need_usable_accounts"`
	RecommendedRequiredUsableAccounts int        `json:"recommended_required_usable_accounts"`
	RecommendedAddUsableAccounts      int        `json:"recommended_add_usable_accounts"`
	RecommendedRegisterAccounts       int        `json:"recommended_register_accounts"`
	LastEvaluationAt                  *time.Time `json:"last_evaluation_at,omitempty"`
	LastActionAt                      *time.Time `json:"last_action_at,omitempty"`
	NextActionAt                      *time.Time `json:"next_action_at,omitempty"`
}

type imagePoolAutoRegistrationState struct {
	initialized bool
	idleSince   time.Time
	lastAction  time.Time
	status      imagePoolAutoRegistrationStatus
}

func (s *Server) runImagePoolAutoRegistration(ctx context.Context) {
	if s == nil {
		return
	}
	s.reconcileImagePoolAutoRegistration(time.Now())
	for {
		cfg := s.currentConfig()
		interval := time.Duration(cfg.ImagePoolAutoRegisterIntervalSecs) * time.Second
		if interval < 5*time.Second {
			interval = 5 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if s.register != nil && s.register.IsAutomatic() {
				s.register.Stop()
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			s.reconcileImagePoolAutoRegistration(time.Now())
		}
	}
}

func (s *Server) reconcileImagePoolAutoRegistration(now time.Time) {
	if s == nil || s.accounts == nil || s.tasks == nil || s.register == nil {
		return
	}
	cfg := s.currentConfig()
	activeTasks := s.tasks.List(nil)
	pressure := summarizeImagePoolTasks(activeTasks, now)
	recentItems := activeTasks
	if history, err := s.tasks.HistoryForOwner(1, 180, "", true); err == nil && len(history.Items) > 0 {
		recentItems = history.Items
	}
	recent := summarizeRecentImageTasks(recentItems, 60)
	accountStats := s.accounts.ImageDispatchStats()
	factors, estimate := estimateImagePoolCapacity(cfg, pressure, recent, accountStats)
	estimate = alignImagePoolRegistrationEstimate(cfg, pressure, factors, accountStats, estimate)

	s.autoRegistrationMu.Lock()
	if pressure.Pending > 0 {
		s.autoRegistration.idleSince = time.Time{}
	} else if s.autoRegistration.idleSince.IsZero() {
		s.autoRegistration.idleSince = now
	}
	idleSince := s.autoRegistration.idleSince
	lastAction := s.autoRegistration.lastAction
	s.autoRegistrationMu.Unlock()

	registrationConfig := s.register.Get()
	running := s.register.IsRunning()
	automatic := s.register.IsAutomatic()
	status := imagePoolAutoRegistrationStatus{
		Enabled:                           cfg.ImagePoolAutoRegisterEnabled,
		Running:                           running,
		Automatic:                         automatic,
		PendingTasks:                      pressure.Pending,
		CurrentUsableAccounts:             accountStats.Usable,
		CurrentDispatchableAccounts:       accountStats.Dispatchable,
		InflightRegistrations:             registrationConfig.Stats.Running,
		RecommendedRequiredUsableAccounts: estimate.RecommendedRequiredUsableAccounts,
		RecommendedAddUsableAccounts:      estimate.RecommendedAddUsableAccounts,
		RecommendedRegisterAccounts:       estimate.RecommendedRegisterAccounts,
		LastEvaluationAt:                  timePtr(now),
		LastActionAt:                      timePtr(lastAction),
	}
	if !idleSince.IsZero() {
		status.IdleForSecs = roundFloat(now.Sub(idleSince).Seconds(), 1)
		if status.IdleForSecs < 0 {
			status.IdleForSecs = 0
		}
	}

	if !cfg.ImagePoolAutoRegisterEnabled {
		if automatic {
			s.register.Stop()
			status.Running = s.register.IsRunning()
			status.Automatic = false
		}
		status.Status = "disabled"
		status.Reason = "自动补号未启用，注册机只接受手动控制。"
		s.setImagePoolAutoRegistrationStatus(status)
		return
	}

	if running && !automatic {
		status.Status = "manual_running"
		status.Reason = "检测到手动注册任务正在运行，自动补号暂不接管。"
		s.setImagePoolAutoRegistrationStatus(status)
		return
	}

	target := estimate.RecommendedRequiredUsableAccounts
	status.TargetUsableAccounts = target
	status.NeedUsableAccounts = maxInt(0, target-accountStats.Usable)

	// When the queue is empty, a completed active batch is enough. The idle
	// floor is only filled during the configured quiet grace period; after that
	// the controller stays silent until a new task arrives.
	if pressure.Pending == 0 {
		quietAfter := time.Duration(cfg.ImagePoolQuietAfterMinutes) * time.Minute
		quiet := !idleSince.IsZero() && now.Sub(idleSince) >= quietAfter
		if accountStats.Usable >= target || quiet {
			if automatic {
				s.register.Stop()
				status.Running = s.register.IsRunning()
				status.Automatic = false
			}
			status.Status = "idle"
			if quiet {
				status.Reason = "任务已连续空闲，已进入静默模式，等待新任务后再补号。"
			} else {
				status.Reason = "当前没有待处理任务，保留现有账号，不启动新的注册批次。"
			}
			s.setImagePoolAutoRegistrationStatus(status)
			return
		}
	}

	if accountStats.Usable >= target {
		if automatic {
			s.register.Stop()
			status.Running = s.register.IsRunning()
			status.Automatic = false
		}
		status.Status = "enough"
		status.Reason = fmt.Sprintf("当前有效账号 %d 个，已达到任务所需目标 %d 个。", accountStats.Usable, target)
		s.setImagePoolAutoRegistrationStatus(status)
		return
	}

	if cfg.ImagePoolMaxUsableAccounts > 0 && accountStats.Usable >= cfg.ImagePoolMaxUsableAccounts {
		status.Status = "max_reached"
		status.Reason = fmt.Sprintf("当前有效账号已达到上限 %d 个，暂不继续注册。", cfg.ImagePoolMaxUsableAccounts)
		s.setImagePoolAutoRegistrationStatus(status)
		return
	}

	if running {
		// An automatic batch owns the current manager run. Re-evaluate its
		// bounded attempt count as the pool changes, but never take over a
		// manually started run.
		batch := autoRegistrationBatchSize(cfg, target, accountStats.Usable)
		if batch <= 0 {
			s.register.Stop()
			status.Status = "enough"
			status.Reason = "当前批次已不再需要，已请求停止注册。"
		} else {
			threads := normalizedRegistrationThreads(registrationConfig.Threads, batch)
			if registrationConfig.Mode != "total" || registrationConfig.Total != batch || registrationConfig.Threads != threads {
				registrationConfig = s.register.Update(map[string]any{"mode": "total", "total": batch, "threads": threads})
			}
			status.Status = "registering"
			status.Reason = fmt.Sprintf("任务压力需要约 %d 个有效账号，当前批次最多尝试注册 %d 个。", target, batch)
			status.BatchTargetUsableAccounts = accountStats.Usable + batch
			status.InflightRegistrations = registrationConfig.Stats.Running
		}
		s.setImagePoolAutoRegistrationStatus(status)
		return
	}

	cooldown := time.Duration(cfg.ImagePoolRegisterCooldownMinutes) * time.Minute
	if !lastAction.IsZero() && now.Before(lastAction.Add(cooldown)) {
		next := lastAction.Add(cooldown)
		status.NextActionAt = timePtr(next)
		status.Status = "cooldown"
		status.Reason = fmt.Sprintf("上一注册批次刚结束，冷却至 %s 后再评估。", next.Format(time.RFC3339))
		s.setImagePoolAutoRegistrationStatus(status)
		return
	}

	batch := autoRegistrationBatchSize(cfg, target, accountStats.Usable)
	if batch <= 0 {
		status.Status = "enough"
		status.Reason = "当前没有需要补充的有效账号。"
		s.setImagePoolAutoRegistrationStatus(status)
		return
	}
	threads := normalizedRegistrationThreads(registrationConfig.Threads, batch)
	s.register.Update(map[string]any{"mode": "total", "total": batch, "threads": threads})
	s.register.StartAutomatic()
	s.autoRegistrationMu.Lock()
	s.autoRegistration.lastAction = now
	s.autoRegistrationMu.Unlock()
	status.Running = s.register.IsRunning()
	status.Automatic = s.register.IsAutomatic()
	status.LastActionAt = timePtr(now)
	status.Status = "registering"
	status.BatchTargetUsableAccounts = accountStats.Usable + batch
	status.InflightRegistrations = s.register.Get().Stats.Running
	status.Reason = fmt.Sprintf("任务压力需要约 %d 个有效账号，已启动最多 %d 次注册尝试。", target, batch)
	s.setImagePoolAutoRegistrationStatus(status)
}

func autoRegistrationTarget(cfg config.Config, pressure imagePoolTaskPressure, factors imagePoolCapacityFactors, estimate imagePoolCapacityEstimate) int {
	target := cfg.ImagePoolIdleFloorAccounts
	if pressure.Pending > 0 {
		slots := maxInt(pressure.Pending, pressure.Running)
		slots = maxInt(slots, estimate.RequiredByQueueDrain)
		slots = maxInt(slots, estimate.RequiredByRecentThroughput)
		slots = maxInt(slots, estimate.RequiredByQuota)
		reserveRatio := factors.DynamicReserveRatio
		reserve := 0
		if slots > 0 {
			reserve = int(math.Ceil(float64(slots) * reserveRatio))
			if slots <= 2 && reserveRatio < 0.25 {
				reserve = 0
			}
		}
		maxInflight := factors.MaxInflightPerAccount
		if maxInflight <= 0 {
			maxInflight = 1
		}
		target = maxInt(target, int(math.Ceil(float64(slots+reserve)/float64(maxInflight))))
		target = maxInt(target, cfg.ImagePoolMinUsableAccounts)
	}
	if cfg.ImagePoolMaxUsableAccounts > 0 && target > cfg.ImagePoolMaxUsableAccounts {
		target = cfg.ImagePoolMaxUsableAccounts
	}
	return maxInt(0, target)
}

func autoRegistrationBatchSize(cfg config.Config, target, current int) int {
	need := maxInt(0, target-current)
	limit := cfg.ImagePoolMaxRegisterPerCycle
	if limit <= 0 {
		limit = 1
	}
	return minInt(need, limit)
}

func normalizedRegistrationThreads(configured, batch int) int {
	if configured < 1 {
		configured = 1
	}
	if batch > 0 && configured > batch {
		configured = batch
	}
	return configured
}

func (s *Server) setImagePoolAutoRegistrationStatus(status imagePoolAutoRegistrationStatus) {
	if s == nil {
		return
	}
	s.autoRegistrationMu.Lock()
	s.autoRegistration.status = status
	s.autoRegistration.initialized = true
	s.autoRegistrationMu.Unlock()
}

func (s *Server) imagePoolAutoRegistrationSnapshot() imagePoolAutoRegistrationStatus {
	if s == nil {
		return imagePoolAutoRegistrationStatus{Status: "disabled", Reason: "号池自动补号控制器未初始化。"}
	}
	cfg := s.currentConfig()
	s.autoRegistrationMu.RLock()
	status := s.autoRegistration.status
	initialized := s.autoRegistration.initialized
	s.autoRegistrationMu.RUnlock()
	if !initialized {
		status = imagePoolAutoRegistrationStatus{
			Enabled: cfg.ImagePoolAutoRegisterEnabled,
			Status:  "disabled",
			Reason:  "自动补号控制器尚未运行。",
		}
	}
	status.Enabled = cfg.ImagePoolAutoRegisterEnabled
	if s.register != nil {
		status.Running = s.register.IsRunning()
		status.Automatic = s.register.IsAutomatic()
		if registrationConfig := s.register.Get(); registrationConfig.Stats.Running > status.InflightRegistrations {
			status.InflightRegistrations = registrationConfig.Stats.Running
		}
	}
	return status
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copied := value
	return &copied
}
