package httpapi

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
	"imagepool/internal/tasks"
)

type imagePoolCapacityResponse struct {
	GeneratedAt time.Time                   `json:"generated_at"`
	Tasks       imagePoolTaskPressure       `json:"tasks"`
	Recent      imagePoolRecentTaskStats    `json:"recent_60_tasks"`
	Accounts    accounts.ImageDispatchStats `json:"accounts"`
	Factors     imagePoolCapacityFactors    `json:"factors"`
	Estimate    imagePoolCapacityEstimate   `json:"estimate"`
}

type imagePoolTaskPressure struct {
	Queued             int     `json:"queued"`
	Running            int     `json:"running"`
	Pending            int     `json:"pending"`
	OldestQueuedSecs   float64 `json:"oldest_queued_secs"`
	OldestRunningSecs  float64 `json:"oldest_running_secs"`
	AverageRunningSecs float64 `json:"average_running_secs"`
	MemoryTaskTotal    int     `json:"memory_task_total"`
}

type imagePoolRecentTaskStats struct {
	Limit                      int     `json:"limit"`
	Total                      int     `json:"total"`
	AvailabilityTotal          int     `json:"availability_total"`
	Success                    int     `json:"success"`
	Failed                     int     `json:"failed"`
	Rejected                   int     `json:"rejected"`
	Canceled                   int     `json:"canceled"`
	Other                      int     `json:"other"`
	SuccessRate                float64 `json:"success_rate"`
	FailureRate                float64 `json:"failure_rate"`
	AverageDurationMS          float64 `json:"average_duration_ms"`
	AverageDurationSecs        float64 `json:"average_duration_secs"`
	AverageSuccessDurationMS   float64 `json:"average_success_duration_ms"`
	AverageSuccessDurationSecs float64 `json:"average_success_duration_secs"`
	AverageFailureDurationMS   float64 `json:"average_failure_duration_ms"`
	AverageFailureDurationSecs float64 `json:"average_failure_duration_secs"`
	DurationSamples            int     `json:"duration_samples"`
	SuccessDurationSamples     int     `json:"success_duration_samples"`
	FailureDurationSamples     int     `json:"failure_duration_samples"`
	ArrivalSamples             int     `json:"arrival_samples"`
	ArrivalSpanSecs            float64 `json:"arrival_span_secs"`
	ArrivalRatePerMin          float64 `json:"arrival_rate_per_min"`
}

type imagePoolCapacityFactors struct {
	ObservedAverageSecs          float64 `json:"observed_average_secs"`
	DrainWindowSecs              float64 `json:"drain_window_secs"`
	SuccessProbability           float64 `json:"success_probability"`
	RetryMultiplier              float64 `json:"retry_multiplier"`
	RecentFailureRate            float64 `json:"recent_failure_rate"`
	HistoricalAccountFailureRate float64 `json:"historical_account_failure_rate"`
	DeadAccountRate              float64 `json:"dead_account_rate"`
	CoolingRate                  float64 `json:"cooling_rate"`
	PressureRatio                float64 `json:"pressure_ratio"`
	DynamicReserveRatio          float64 `json:"dynamic_reserve_ratio"`
	RegistrationAdjustmentFactor float64 `json:"registration_adjustment_factor"`
}

type imagePoolCapacityEstimate struct {
	RequiredByCurrentParallel         int     `json:"required_by_current_parallel"`
	RequiredByRecentThroughput        int     `json:"required_by_recent_throughput"`
	RequiredByQueueDrain              int     `json:"required_by_queue_drain"`
	RequiredByBurstParallel           int     `json:"required_by_burst_parallel"`
	RequiredByQuota                   int     `json:"required_by_quota"`
	RecommendedRequiredUsableAccounts int     `json:"recommended_required_usable_accounts"`
	CurrentEffectiveAccounts          int     `json:"current_effective_accounts"`
	RecommendedAddUsableAccounts      int     `json:"recommended_add_usable_accounts"`
	RecommendedRegisterAccounts       int     `json:"recommended_register_accounts"`
	ExpectedAttemptsForPendingTasks   float64 `json:"expected_attempts_for_pending_tasks"`
	EstimatedQuotaCapacity            float64 `json:"estimated_quota_capacity"`
	AverageQuotaPerUsableAccount      float64 `json:"average_quota_per_usable_account"`
	Status                            string  `json:"status"`
	Message                           string  `json:"message"`
}

func (s *Server) handleImagePoolCapacity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	limit := boundedQueryInt(r, "limit", 60, 10, 200)
	now := time.Now()
	activeTasks := s.tasks.List(nil)
	history, err := s.tasks.HistoryForOwner(1, maxInt(limit*3, limit), "", true)
	recentItems := history.Items
	if err != nil || len(recentItems) == 0 {
		recentItems = activeTasks
	}
	taskPressure := summarizeImagePoolTasks(activeTasks, now)
	recent := summarizeRecentImageTasks(recentItems, limit)
	accountStats := s.accounts.ImageDispatchStats()
	cfg := s.currentConfig()
	if r.URL.Query().Has("burst_parallel") {
		cfg.ImageCapacityBurstParallel = boundedQueryInt(r, "burst_parallel", cfg.ImageCapacityBurstParallel, 1, 10000)
	}
	factors, estimate := estimateImagePoolCapacity(cfg, taskPressure, recent, accountStats)
	writeJSON(w, http.StatusOK, imagePoolCapacityResponse{
		GeneratedAt: now,
		Tasks:       taskPressure,
		Recent:      recent,
		Accounts:    accountStats,
		Factors:     factors,
		Estimate:    estimate,
	})
}

func summarizeImagePoolTasks(items []tasks.Task, now time.Time) imagePoolTaskPressure {
	stats := imagePoolTaskPressure{MemoryTaskTotal: len(items)}
	var runningSum float64
	for _, task := range items {
		switch task.Status {
		case tasks.StatusQueued:
			stats.Queued++
			age := taskAgeSecs(task.CreatedAt, now)
			if age > stats.OldestQueuedSecs {
				stats.OldestQueuedSecs = age
			}
		case tasks.StatusRunning:
			stats.Running++
			started := task.CreatedAt
			if task.StartedAt != nil && !task.StartedAt.IsZero() {
				started = *task.StartedAt
			}
			age := taskAgeSecs(started, now)
			runningSum += age
			if age > stats.OldestRunningSecs {
				stats.OldestRunningSecs = age
			}
		}
	}
	stats.Pending = stats.Queued + stats.Running
	if stats.Running > 0 {
		stats.AverageRunningSecs = roundFloat(runningSum/float64(stats.Running), 2)
	}
	stats.OldestQueuedSecs = roundFloat(stats.OldestQueuedSecs, 2)
	stats.OldestRunningSecs = roundFloat(stats.OldestRunningSecs, 2)
	return stats
}

func summarizeRecentImageTasks(items []tasks.Task, limit int) imagePoolRecentTaskStats {
	if limit <= 0 {
		limit = 60
	}
	stats := imagePoolRecentTaskStats{Limit: limit}
	if len(items) == 0 {
		return stats
	}
	items = append([]tasks.Task(nil), items...)
	sort.Slice(items, func(i, j int) bool {
		left, right := taskSortTime(items[i]), taskSortTime(items[j])
		if left.Equal(right) {
			return items[i].ID > items[j].ID
		}
		return left.After(right)
	})
	var durationSum, successDurationSum, failureDurationSum int64
	var newestCreated, oldestCreated time.Time
	for _, task := range items {
		if stats.ArrivalSamples < limit && !task.CreatedAt.IsZero() {
			if newestCreated.IsZero() || task.CreatedAt.After(newestCreated) {
				newestCreated = task.CreatedAt
			}
			if oldestCreated.IsZero() || task.CreatedAt.Before(oldestCreated) {
				oldestCreated = task.CreatedAt
			}
			stats.ArrivalSamples++
		}
		if stats.Total >= limit {
			continue
		}
		category := taskCapacityCategory(task)
		if category == "running" {
			continue
		}
		stats.Total++
		durationMS := taskDurationMS(task)
		switch category {
		case "success":
			stats.Success++
			stats.AvailabilityTotal++
			if durationMS > 0 {
				stats.DurationSamples++
				stats.SuccessDurationSamples++
				durationSum += durationMS
				successDurationSum += durationMS
			}
		case "failed":
			stats.Failed++
			stats.AvailabilityTotal++
			if durationMS > 0 {
				stats.DurationSamples++
				stats.FailureDurationSamples++
				durationSum += durationMS
				failureDurationSum += durationMS
			}
		case "rejected":
			stats.Rejected++
		case "canceled":
			stats.Canceled++
		default:
			stats.Other++
		}
	}
	if stats.AvailabilityTotal > 0 {
		stats.SuccessRate = roundFloat(float64(stats.Success)*100/float64(stats.AvailabilityTotal), 2)
		stats.FailureRate = roundFloat(float64(stats.Failed)*100/float64(stats.AvailabilityTotal), 2)
	}
	if stats.DurationSamples > 0 {
		stats.AverageDurationMS = roundFloat(float64(durationSum)/float64(stats.DurationSamples), 2)
		stats.AverageDurationSecs = roundFloat(stats.AverageDurationMS/1000, 2)
	}
	if stats.SuccessDurationSamples > 0 {
		stats.AverageSuccessDurationMS = roundFloat(float64(successDurationSum)/float64(stats.SuccessDurationSamples), 2)
		stats.AverageSuccessDurationSecs = roundFloat(stats.AverageSuccessDurationMS/1000, 2)
	}
	if stats.FailureDurationSamples > 0 {
		stats.AverageFailureDurationMS = roundFloat(float64(failureDurationSum)/float64(stats.FailureDurationSamples), 2)
		stats.AverageFailureDurationSecs = roundFloat(stats.AverageFailureDurationMS/1000, 2)
	}
	if stats.ArrivalSamples > 1 && !newestCreated.IsZero() && !oldestCreated.IsZero() {
		span := newestCreated.Sub(oldestCreated).Seconds()
		if span > 0 {
			stats.ArrivalSpanSecs = roundFloat(span, 2)
			stats.ArrivalRatePerMin = roundFloat(float64(stats.ArrivalSamples)*60/span, 2)
		}
	}
	return stats
}

func estimateImagePoolCapacity(cfg config.Config, pressure imagePoolTaskPressure, recent imagePoolRecentTaskStats, accountStats accounts.ImageDispatchStats) (imagePoolCapacityFactors, imagePoolCapacityEstimate) {
	avgSecs := observedImageAverageSecs(cfg, recent)
	successProbability := imageSuccessProbability(recent, accountStats)
	retryMultiplier := 1 / successProbability
	deadRate := accountStats.DeadRate / 100
	coolingRate := accountStats.CoolingRate / 100
	recentFailureRate := 1 - successProbability
	if recent.AvailabilityTotal > 0 {
		recentFailureRate = recent.FailureRate / 100
	}
	pressureRatio := 0.0
	if accountStats.Dispatchable > 0 {
		pressureRatio = float64(pressure.Pending) / float64(accountStats.Dispatchable)
	} else if pressure.Pending > 0 {
		pressureRatio = float64(pressure.Pending)
	}
	drainWindowSecs := dynamicDrainWindowSecs(cfg, avgSecs, pressure)
	requiredByQueueDrain := 0
	expectedAttempts := 0.0
	if pressure.Pending > 0 {
		expectedAttempts = float64(pressure.Pending) * retryMultiplier
		requiredByQueueDrain = int(math.Ceil(float64(pressure.Pending) * avgSecs * retryMultiplier / drainWindowSecs))
		if requiredByQueueDrain < pressure.Running {
			requiredByQueueDrain = pressure.Running
		}
	}
	requiredByThroughput := 0
	if recent.ArrivalRatePerMin > 0 {
		requiredByThroughput = int(math.Ceil((recent.ArrivalRatePerMin / 60) * avgSecs * retryMultiplier))
		if pressure.Pending > 0 && requiredByThroughput > pressure.Pending {
			requiredByThroughput = pressure.Pending
		}
	}
	requiredByParallel := pressure.Pending
	avgQuota := dynamicAverageQuotaPerUsableAccount(accountStats)
	estimatedQuotaCapacity := float64(accountStats.KnownRemainingQuota)
	if accountStats.UnknownQuotaUsable > 0 {
		estimatedQuotaCapacity += float64(accountStats.UnknownQuotaUsable) * avgQuota
	}
	requiredByQuota := 0
	if expectedAttempts > 0 && avgQuota > 0 && estimatedQuotaCapacity < expectedAttempts {
		requiredByQuota = int(math.Ceil(expectedAttempts / avgQuota))
	}
	requiredByBurst := cfg.ImageCapacityBurstParallel
	if requiredByBurst <= 0 {
		requiredByBurst = config.Default().ImageCapacityBurstParallel
	}
	baseRequired := maxInt(pressure.Running, requiredByQueueDrain)
	baseRequired = maxInt(baseRequired, requiredByQuota)
	if pressure.Pending == 0 {
		baseRequired = maxInt(baseRequired, requiredByThroughput)
	} else {
		baseRequired = maxInt(baseRequired, minInt(requiredByThroughput, pressure.Pending))
	}
	reserveRatio := dynamicCapacityReserveRatio(recent, recentFailureRate, coolingRate, pressureRatio)
	dynamicRequired := baseRequired
	if baseRequired > 0 {
		reserveAccounts := int(math.Ceil(float64(baseRequired) * reserveRatio))
		if baseRequired <= 2 && reserveRatio < 0.25 {
			reserveAccounts = 0
		}
		dynamicRequired += reserveAccounts
	}
	recommendedRequired := maxInt(dynamicRequired, requiredByBurst)
	currentEffective := accountStats.Dispatchable
	addUsable := maxInt(0, recommendedRequired-currentEffective)
	registrationFactor := registrationAdjustmentFactor(deadRate)
	registerAccounts := 0
	if addUsable > 0 {
		registerAccounts = int(math.Ceil(float64(addUsable) * registrationFactor))
	}
	factors := imagePoolCapacityFactors{
		ObservedAverageSecs:          roundFloat(avgSecs, 2),
		DrainWindowSecs:              roundFloat(drainWindowSecs, 2),
		SuccessProbability:           roundFloat(successProbability, 4),
		RetryMultiplier:              roundFloat(retryMultiplier, 4),
		RecentFailureRate:            roundFloat(recentFailureRate*100, 2),
		HistoricalAccountFailureRate: accountStats.HistoricalFailureRate,
		DeadAccountRate:              accountStats.DeadRate,
		CoolingRate:                  accountStats.CoolingRate,
		PressureRatio:                roundFloat(pressureRatio, 2),
		DynamicReserveRatio:          roundFloat(reserveRatio, 4),
		RegistrationAdjustmentFactor: roundFloat(registrationFactor, 4),
	}
	estimate := imagePoolCapacityEstimate{
		RequiredByCurrentParallel:         requiredByParallel,
		RequiredByRecentThroughput:        requiredByThroughput,
		RequiredByQueueDrain:              requiredByQueueDrain,
		RequiredByBurstParallel:           requiredByBurst,
		RequiredByQuota:                   requiredByQuota,
		RecommendedRequiredUsableAccounts: recommendedRequired,
		CurrentEffectiveAccounts:          currentEffective,
		RecommendedAddUsableAccounts:      addUsable,
		RecommendedRegisterAccounts:       registerAccounts,
		ExpectedAttemptsForPendingTasks:   roundFloat(expectedAttempts, 2),
		EstimatedQuotaCapacity:            roundFloat(estimatedQuotaCapacity, 2),
		AverageQuotaPerUsableAccount:      roundFloat(avgQuota, 2),
	}
	estimate.Status, estimate.Message = imagePoolCapacityStatus(pressure, accountStats, estimate)
	return factors, estimate
}

func observedImageAverageSecs(cfg config.Config, recent imagePoolRecentTaskStats) float64 {
	switch {
	case recent.AverageSuccessDurationSecs > 0:
		return recent.AverageSuccessDurationSecs
	case recent.AverageDurationSecs > 0:
		return recent.AverageDurationSecs
	}
	poll := cfg.ImagePollTimeoutSecs
	if poll <= 0 {
		poll = cfg.ImageTaskTimeoutSecs
	}
	if poll <= 0 {
		poll = cfg.RequestTimeoutSecs
	}
	if poll <= 0 {
		return 60
	}
	return math.Max(10, poll/2)
}

func imageSuccessProbability(recent imagePoolRecentTaskStats, accountStats accounts.ImageDispatchStats) float64 {
	recentProb := 0.0
	if recent.AvailabilityTotal > 0 {
		recentProb = recent.SuccessRate / 100
	}
	historicalProb := 0.0
	if total := accountStats.TotalImageSuccess + accountStats.TotalImageFailures; total > 0 {
		historicalProb = accountStats.HistoricalSuccessRate / 100
	}
	poolSurvival := 1 - accountStats.DeadRate/100
	switch {
	case recent.AvailabilityTotal >= 20:
		return clampFloat(recentProb*0.7+nonZeroProbability(historicalProb, recentProb)*0.2+poolSurvival*0.1, 0.2, 1)
	case recent.AvailabilityTotal > 0:
		return clampFloat(recentProb*0.5+nonZeroProbability(historicalProb, recentProb)*0.3+poolSurvival*0.2, 0.2, 1)
	case historicalProb > 0:
		return clampFloat(historicalProb*0.6+poolSurvival*0.4, 0.2, 1)
	default:
		return clampFloat(poolSurvival, 0.2, 1)
	}
}

func nonZeroProbability(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	if fallback > 0 {
		return fallback
	}
	return 1
}

func dynamicDrainWindowSecs(cfg config.Config, avgSecs float64, pressure imagePoolTaskPressure) float64 {
	if avgSecs <= 0 {
		avgSecs = observedImageAverageSecs(cfg, imagePoolRecentTaskStats{})
	}
	if pressure.Queued == 0 {
		return avgSecs
	}
	budget := cfg.ImageTaskTimeoutSecs
	if budget <= 0 {
		budget = cfg.ImagePollTimeoutSecs + cfg.RequestTimeoutSecs
	}
	if budget <= 0 {
		budget = avgSecs * 3
	}
	remainingBudget := budget - pressure.OldestQueuedSecs
	if remainingBudget < avgSecs {
		remainingBudget = avgSecs
	}
	observedWindow := avgSecs * (1 + math.Log1p(float64(maxInt(pressure.Pending, 1)))/4)
	if observedWindow < avgSecs {
		observedWindow = avgSecs
	}
	if observedWindow > remainingBudget {
		return remainingBudget
	}
	return observedWindow
}

func dynamicCapacityReserveRatio(recent imagePoolRecentTaskStats, recentFailureRate, coolingRate, pressureRatio float64) float64 {
	sampleUncertainty := 0.0
	if recent.AvailabilityTotal < 20 {
		sampleUncertainty = float64(20-recent.AvailabilityTotal) / 200
	}
	pressureReserve := math.Min(0.6, pressureRatio*0.08)
	value := recentFailureRate*0.8 + coolingRate*0.5 + pressureReserve + sampleUncertainty
	return clampFloat(value, 0, 1.5)
}

func dynamicAverageQuotaPerUsableAccount(accountStats accounts.ImageDispatchStats) float64 {
	if accountStats.AverageKnownRemainingQuota > 0 {
		return accountStats.AverageKnownRemainingQuota
	}
	if accountStats.KnownRemainingQuota > 0 && accountStats.KnownQuotaAccounts > 0 {
		return float64(accountStats.KnownRemainingQuota) / float64(accountStats.KnownQuotaAccounts)
	}
	if accountStats.Usable > 0 && accountStats.KnownRemainingQuota > 0 {
		return float64(accountStats.KnownRemainingQuota) / float64(accountStats.Usable)
	}
	return 1
}

func registrationAdjustmentFactor(deadRate float64) float64 {
	survival := 1 - deadRate
	if survival < 0.1 {
		survival = 0.1
	}
	return 1 / survival
}

func imagePoolCapacityStatus(pressure imagePoolTaskPressure, accountStats accounts.ImageDispatchStats, estimate imagePoolCapacityEstimate) (string, string) {
	switch {
	case pressure.Pending == 0 && estimate.RecommendedAddUsableAccounts == 0:
		return "idle", "当前没有待处理生图任务，暂不需要补号。"
	case estimate.RecommendedAddUsableAccounts > 0:
		return "shortage", fmt.Sprintf("当前有效号池不足，建议补充约 %d 个可用账号；按当前死号率折算注册机建议注册 %d 个。", estimate.RecommendedAddUsableAccounts, estimate.RecommendedRegisterAccounts)
	case pressure.Queued > 0 && accountStats.Idle == 0:
		return "saturated", "当前任务正在排队，但现有有效账号数按动态模型仍可消化，建议观察下一轮容量评估。"
	default:
		return "enough", "当前号池容量足够支撑现有任务，暂不需要注册机补号。"
	}
}

func taskCapacityCategory(task tasks.Task) string {
	if isContentPolicyErrorMessage(task.Error) || isContentPolicyErrorMessage(task.RealtimeStatus) {
		return "rejected"
	}
	if task.Progress == "canceled" {
		return "canceled"
	}
	switch task.Status {
	case tasks.StatusSucceeded:
		return "success"
	case tasks.StatusQueued, tasks.StatusRunning:
		return "running"
	case tasks.StatusFailed:
		return "failed"
	default:
		return "other"
	}
}

func taskDurationMS(task tasks.Task) int64 {
	if task.DurationMS > 0 {
		return task.DurationMS
	}
	if task.StartedAt != nil && task.FinishedAt != nil && !task.StartedAt.IsZero() && !task.FinishedAt.IsZero() {
		return task.FinishedAt.Sub(*task.StartedAt).Milliseconds()
	}
	if task.FinishedAt != nil && !task.CreatedAt.IsZero() && !task.FinishedAt.IsZero() {
		return task.FinishedAt.Sub(task.CreatedAt).Milliseconds()
	}
	return 0
}

func taskSortTime(task tasks.Task) time.Time {
	if task.FinishedAt != nil && !task.FinishedAt.IsZero() {
		return *task.FinishedAt
	}
	if !task.UpdatedAt.IsZero() {
		return task.UpdatedAt
	}
	return task.CreatedAt
}

func taskAgeSecs(start, now time.Time) float64 {
	if start.IsZero() || now.IsZero() || now.Before(start) {
		return 0
	}
	return now.Sub(start).Seconds()
}

func boundedQueryInt(r *http.Request, key string, fallback, minValue, maxValue int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil {
		value = fallback
	}
	if value < minValue {
		value = minValue
	}
	if value > maxValue {
		value = maxValue
	}
	return value
}

func clampFloat(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func roundFloat(value float64, places int) float64 {
	if places < 0 {
		places = 0
	}
	factor := math.Pow10(places)
	return math.Round(value*factor) / factor
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
