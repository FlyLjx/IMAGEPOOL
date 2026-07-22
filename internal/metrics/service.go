package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"imagepool/internal/errorinfo"
	"imagepool/internal/persistence"
)

const (
	defaultMaxEntries = 5000
	persistDebounce   = 150 * time.Millisecond
)

type Call struct {
	ID             string         `json:"id"`
	Time           time.Time      `json:"time"`
	Endpoint       string         `json:"endpoint"`
	Model          string         `json:"model,omitempty"`
	Status         string         `json:"status"`
	StatusCode     int            `json:"status_code"`
	DurationMS     int64          `json:"duration_ms,omitempty"`
	Error          string         `json:"error,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorTitle     string         `json:"error_title,omitempty"`
	ErrorCategory  string         `json:"error_category,omitempty"`
	ErrorRetryable bool           `json:"error_retryable,omitempty"`
	ErrorAction    string         `json:"error_action,omitempty"`
	ErrorHint      string         `json:"error_hint,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	Details        map[string]any `json:"detail,omitempty"`
}

type StabilityPoint struct {
	Time    time.Time `json:"time"`
	Success int       `json:"success"`
	Failed  int       `json:"failed"`
}

type Stability struct {
	WindowSeconds    int              `json:"window_seconds"`
	WindowStart      time.Time        `json:"window_start"`
	WindowEnd        time.Time        `json:"window_end"`
	GeneratedAt      time.Time        `json:"generated_at"`
	Total            int              `json:"total"`
	Success          int              `json:"success"`
	Failed           int              `json:"failed"`
	StabilityPercent float64          `json:"stability_percent"`
	Status           string           `json:"status"`
	Series           []StabilityPoint `json:"series"`
	Recent60         RecentCallStats  `json:"recent_60"`
}

type RecentCallStats struct {
	Limit                      int       `json:"limit"`
	Total                      int       `json:"total"`
	AvailabilityTotal          int       `json:"availability_total"`
	Success                    int       `json:"success"`
	Failed                     int       `json:"failed"`
	Rejected                   int       `json:"rejected"`
	Canceled                   int       `json:"canceled"`
	Running                    int       `json:"running"`
	Other                      int       `json:"other"`
	SuccessRate                float64   `json:"success_rate"`
	FailureRate                float64   `json:"failure_rate"`
	AverageDurationMS          float64   `json:"average_duration_ms"`
	AverageDurationSecs        float64   `json:"average_duration_secs"`
	AverageSuccessDurationMS   float64   `json:"average_success_duration_ms"`
	AverageSuccessDurationSecs float64   `json:"average_success_duration_secs"`
	AverageFailureDurationMS   float64   `json:"average_failure_duration_ms"`
	AverageFailureDurationSecs float64   `json:"average_failure_duration_secs"`
	DurationSamples            int       `json:"duration_samples"`
	SuccessDurationSamples     int       `json:"success_duration_samples"`
	FailureDurationSamples     int       `json:"failure_duration_samples"`
	GeneratedAt                time.Time `json:"generated_at"`
}

type Service struct {
	mu       sync.RWMutex
	persist  sync.Mutex
	path     string
	state    persistence.Store
	max      int
	calls    []Call
	now      func() time.Time
	sequence uint64
	dirty    bool
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	close    sync.Once
}

func NewService(path string) *Service {
	return newService(path, nil)
}

func NewServiceWithPersistence(state persistence.Store) *Service {
	return newService("", state)
}

func newService(path string, state persistence.Store) *Service {
	s := &Service{path: strings.TrimSpace(path), state: state, max: defaultMaxEntries, now: time.Now}
	_ = s.load()
	if s.path != "" || s.state != nil {
		s.wake = make(chan struct{}, 1)
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		go s.persistenceLoop()
	}
	return s
}

func (s *Service) Record(call Call) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if call.Time.IsZero() {
		call.Time = s.now()
	}
	if call.ID == "" {
		s.sequence++
		call.ID = fmt.Sprintf("call_%d_%d", call.Time.UnixNano(), s.sequence)
	}
	call.Endpoint = strings.TrimSpace(call.Endpoint)
	call.Model = strings.TrimSpace(call.Model)
	call.Error = strings.TrimSpace(call.Error)
	call.ErrorCode = strings.TrimSpace(call.ErrorCode)
	call.ErrorTitle = strings.TrimSpace(call.ErrorTitle)
	call.ErrorCategory = strings.TrimSpace(call.ErrorCategory)
	call.ErrorAction = strings.TrimSpace(call.ErrorAction)
	call.ErrorHint = strings.TrimSpace(call.ErrorHint)
	if call.Status == "" {
		if call.StatusCode >= 400 {
			call.Status = "failed"
		} else {
			call.Status = "success"
		}
	}
	if call.Summary == "" {
		call.Summary = call.Endpoint
		if call.Model != "" {
			call.Summary += " " + call.Model
		}
	}
	s.calls = append(s.calls, call)
	if len(s.calls) > s.max {
		s.calls = append([]Call(nil), s.calls[len(s.calls)-s.max:]...)
	}
	s.dirty = true
	s.mu.Unlock()
	s.signalPersistence()
}

func (s *Service) List(kind, startDate, endDate string) []Call {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Call, 0, len(s.calls))
	for _, call := range s.calls {
		if kind != "" && call.Status != kind {
			continue
		}
		date := call.Time.Local().Format("2006-01-02")
		if startDate != "" && date < startDate {
			continue
		}
		if endDate != "" && date > endDate {
			continue
		}
		out = append(out, call)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

func (s *Service) Delete(ids []string) int {
	if s == nil {
		return 0
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	s.mu.Lock()
	if len(wanted) == 0 {
		removed := len(s.calls)
		s.calls = nil
		s.dirty = true
		s.mu.Unlock()
		s.signalPersistence()
		return removed
	}
	next := s.calls[:0]
	removed := 0
	for _, call := range s.calls {
		if wanted[call.ID] {
			removed++
			continue
		}
		next = append(next, call)
	}
	s.calls = next
	if removed > 0 {
		s.dirty = true
	}
	s.mu.Unlock()
	if removed > 0 {
		s.signalPersistence()
	}
	return removed
}

func (s *Service) Summary(window time.Duration) map[string]any {
	if window <= 0 {
		window = time.Hour
	}
	now := s.now()
	start := now.Add(-window)
	bucket := runtimeBucket(window)
	items := s.List("", "", "")
	byStatus := map[string]int{}
	byEndpoint := map[string]int{}
	byModel := map[string]int{}
	byReason := map[string]errorReasonCount{}
	totals := map[string]int{"success": 0, "failed": 0, "canceled": 0, "rejected": 0, "running": 0, "other": 0}
	bucketCounts := map[string]map[string]int{}
	recentFailed := []map[string]any{}
	for _, call := range items {
		byStatus[call.Status]++
		byEndpoint[call.Endpoint]++
		if call.Model != "" {
			byModel[call.Model]++
		}
		if call.Time.Before(start) {
			continue
		}
		category := callCategory(call)
		totals[category]++
		bucketTime := runtimeBucketTime(call.Time, bucket)
		bucketKey := bucketTime.Format(time.RFC3339)
		if bucketCounts[bucketKey] == nil {
			bucketCounts[bucketKey] = map[string]int{}
		}
		bucketCounts[bucketKey][category]++
		if category == "failed" {
			classified := callErrorInfo(call)
			reason := byReason[classified.Code]
			reason.Code = classified.Code
			reason.Label = classified.Title
			reason.Category = classified.Category
			reason.Value++
			byReason[classified.Code] = reason
			if len(recentFailed) < 20 {
				recentFailed = append(recentFailed, recentFailedCall(call, classified))
			}
		}
	}
	bucketCount := int(window / bucket)
	if window%bucket != 0 {
		bucketCount++
	}
	if bucketCount < 1 {
		bucketCount = 1
	}
	series := make([]map[string]any, 0, bucketCount)
	lastBucket := runtimeBucketTime(now, bucket)
	for offset := bucketCount - 1; offset >= 0; offset-- {
		at := lastBucket.Add(-time.Duration(offset) * bucket)
		key := at.Format(time.RFC3339)
		counts := bucketCounts[key]
		series = append(series, map[string]any{"time": at, "label": runtimeBucketLabel(at, window, bucket), "success": counts["success"], "failed": counts["failed"]})
	}
	statusPie := []map[string]any{}
	for _, status := range []string{"success", "failed", "canceled", "rejected", "running", "other"} {
		if totals[status] > 0 {
			statusPie = append(statusPie, map[string]any{"label": status, "value": totals[status], "status": status})
		}
	}
	reasons := sortedErrorReasons(byReason)
	total := totals["success"] + totals["failed"] + totals["canceled"] + totals["rejected"] + totals["running"] + totals["other"]
	availabilityTotal := totals["success"] + totals["failed"]
	successRate, errorRate := 0.0, 0.0
	if availabilityTotal > 0 {
		successRate = float64(totals["success"]) * 100 / float64(availabilityTotal)
		errorRate = float64(totals["failed"]) * 100 / float64(availabilityTotal)
	}
	return map[string]any{
		"date": now.Local().Format("2006-01-02"), "total": len(items), "by_status": byStatus, "by_endpoint": byEndpoint, "by_model": byModel,
		"runtime":       map[string]any{"window_minutes": int(window / time.Minute), "bucket_minutes": int(bucket / time.Minute), "start_time": start, "end_time": now, "total": total, "success_rate": successRate, "error_rate": errorRate, "totals": totals, "series": series, "status_pie": statusPie, "error_reasons": reasons},
		"recent_failed": recentFailed,
	}
}

func (s *Service) TodaySummary() map[string]any {
	now := s.now()
	localNow := now.In(time.Local)
	start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	byStatus := map[string]int{}
	byEndpoint := map[string]int{}
	byModel := map[string]int{}
	totals := map[string]int{"success": 0, "failed": 0, "canceled": 0, "rejected": 0, "running": 0, "other": 0}
	recentFailed := []map[string]any{}
	for _, call := range s.List("", "", "") {
		if call.Time.Before(start) || call.Time.After(now) {
			continue
		}
		category := callCategory(call)
		totals[category]++
		byStatus[category]++
		byEndpoint[call.Endpoint]++
		if call.Model != "" {
			byModel[call.Model]++
		}
		if category == "failed" && len(recentFailed) < 20 {
			recentFailed = append(recentFailed, recentFailedCall(call, callErrorInfo(call)))
		}
	}
	total := totals["success"] + totals["failed"] + totals["canceled"] + totals["rejected"] + totals["running"] + totals["other"]
	availabilityTotal := totals["success"] + totals["failed"]
	successRate, errorRate := 0.0, 0.0
	if availabilityTotal > 0 {
		successRate = float64(totals["success"]) * 100 / float64(availabilityTotal)
		errorRate = float64(totals["failed"]) * 100 / float64(availabilityTotal)
	}
	return map[string]any{
		"date":               localNow.Format("2006-01-02"),
		"start_time":         start,
		"end_time":           now,
		"total":              total,
		"availability_total": availabilityTotal,
		"success_rate":       successRate,
		"error_rate":         errorRate,
		"totals":             totals,
		"by_status":          byStatus,
		"by_endpoint":        byEndpoint,
		"by_model":           byModel,
		"recent_failed":      recentFailed,
	}
}

// Stability returns a rolling view of completed API calls. It is intentionally
// computed from the current time on every request so callers can poll it
// continuously without waiting for a periodic aggregation job.
func (s *Service) Stability(window time.Duration) Stability {
	if window <= 0 {
		window = time.Minute
	}
	now := s.now()
	start := now.Add(-window)
	seconds := int(window / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	lastSecond := now.Truncate(time.Second)
	firstSecond := lastSecond.Add(-time.Duration(seconds-1) * time.Second)
	points := make([]StabilityPoint, seconds)
	bySecond := make(map[time.Time]int, seconds)
	for index := range points {
		at := firstSecond.Add(time.Duration(index) * time.Second)
		points[index] = StabilityPoint{Time: at}
		bySecond[at] = index
	}

	result := Stability{
		WindowSeconds: seconds,
		WindowStart:   start,
		WindowEnd:     now,
		GeneratedAt:   now,
		Status:        "idle",
		Series:        points,
		Recent60:      s.RecentStats(60),
	}
	for _, call := range s.List("", "", "") {
		if call.Time.Before(start) || call.Time.After(now) {
			continue
		}
		bucket := call.Time.Truncate(time.Second)
		index, inSeries := bySecond[bucket]
		switch callCategory(call) {
		case "success":
			result.Success++
			if inSeries {
				result.Series[index].Success++
			}
		case "failed":
			result.Failed++
			if inSeries {
				result.Series[index].Failed++
			}
		default:
			continue
		}
	}
	result.Total = result.Success + result.Failed
	if result.Total == 0 {
		result.StabilityPercent = 100
		return result
	}
	result.StabilityPercent = float64(result.Success) * 100 / float64(result.Total)
	switch {
	case result.Failed == 0:
		result.Status = "stable"
	case result.Success == 0:
		result.Status = "unstable"
	default:
		result.Status = "degraded"
	}
	return result
}

func (s *Service) RecentStats(limit int) RecentCallStats {
	if limit <= 0 {
		limit = 60
	}
	stats := RecentCallStats{Limit: limit}
	if s == nil {
		return stats
	}
	stats.GeneratedAt = s.now()
	var durationSum, successDurationSum, failureDurationSum int64
	for _, call := range s.List("", "", "") {
		if stats.Total >= limit {
			break
		}
		stats.Total++
		category := callCategory(call)
		switch category {
		case "success":
			stats.Success++
			stats.AvailabilityTotal++
			if call.DurationMS > 0 {
				stats.DurationSamples++
				stats.SuccessDurationSamples++
				durationSum += call.DurationMS
				successDurationSum += call.DurationMS
			}
		case "failed":
			stats.Failed++
			stats.AvailabilityTotal++
			if call.DurationMS > 0 {
				stats.DurationSamples++
				stats.FailureDurationSamples++
				durationSum += call.DurationMS
				failureDurationSum += call.DurationMS
			}
		case "rejected":
			stats.Rejected++
		case "canceled":
			stats.Canceled++
		case "running":
			stats.Running++
		default:
			stats.Other++
		}
	}
	if stats.AvailabilityTotal > 0 {
		stats.SuccessRate = float64(stats.Success) * 100 / float64(stats.AvailabilityTotal)
		stats.FailureRate = float64(stats.Failed) * 100 / float64(stats.AvailabilityTotal)
	}
	if stats.DurationSamples > 0 {
		stats.AverageDurationMS = float64(durationSum) / float64(stats.DurationSamples)
		stats.AverageDurationSecs = stats.AverageDurationMS / 1000
	}
	if stats.SuccessDurationSamples > 0 {
		stats.AverageSuccessDurationMS = float64(successDurationSum) / float64(stats.SuccessDurationSamples)
		stats.AverageSuccessDurationSecs = stats.AverageSuccessDurationMS / 1000
	}
	if stats.FailureDurationSamples > 0 {
		stats.AverageFailureDurationMS = float64(failureDurationSum) / float64(stats.FailureDurationSamples)
		stats.AverageFailureDurationSecs = stats.AverageFailureDurationMS / 1000
	}
	return stats
}

func runtimeBucket(window time.Duration) time.Duration {
	switch {
	case window <= time.Hour:
		return time.Minute
	case window <= 24*time.Hour:
		return 15 * time.Minute
	case window <= 7*24*time.Hour:
		return time.Hour
	default:
		return 24 * time.Hour
	}
}

func runtimeBucketTime(value time.Time, bucket time.Duration) time.Time {
	local := value.Local()
	if bucket >= 24*time.Hour {
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	}
	return local.Truncate(bucket)
}

func runtimeBucketLabel(at time.Time, window, bucket time.Duration) string {
	if bucket >= 24*time.Hour {
		return at.Format("01-02")
	}
	if window > 24*time.Hour {
		return at.Format("01-02 15:04")
	}
	return at.Format("15:04")
}

func (s *Service) load() error {
	if s.state != nil {
		if err := s.state.Load(context.Background(), "calls", &s.calls); err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return err
		}
		return nil
	}
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var calls []Call
	if err := json.Unmarshal(data, &calls); err != nil {
		return err
	}
	s.calls = calls
	return nil
}

func (s *Service) signalPersistence() {
	if s == nil || s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) persistenceLoop() {
	defer close(s.done)
	for {
		select {
		case <-s.wake:
			timer := time.NewTimer(persistDebounce)
			select {
			case <-timer.C:
				_ = s.persistPending()
			case <-s.stop:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				_ = s.persistPending()
				return
			}
		case <-s.stop:
			_ = s.persistPending()
			return
		}
	}
}

// Flush writes the current in-memory call history. Normal request handling
// uses the debounce loop above; this is intended for tests and graceful
// shutdown so no completed request is lost when the container exits.
func (s *Service) Flush() error {
	if s == nil {
		return nil
	}
	for {
		err := s.persistPending()
		if err != nil {
			return err
		}
		s.mu.RLock()
		dirty := s.dirty
		s.mu.RUnlock()
		if !dirty {
			return nil
		}
	}
}

// Close stops the persistence worker after flushing pending metrics. It does
// not close the shared persistence.Store, which is owned by the application.
func (s *Service) Close() {
	if s == nil || s.stop == nil {
		return
	}
	s.close.Do(func() {
		close(s.stop)
		<-s.done
		_ = s.Flush()
	})
}

func (s *Service) persistPending() error {
	if s == nil {
		return nil
	}
	s.persist.Lock()
	defer s.persist.Unlock()

	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	calls := append([]Call(nil), s.calls...)
	s.dirty = false
	s.mu.Unlock()

	err := s.save(calls)
	if err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
	return err
}

func (s *Service) save(calls []Call) error {
	if s.state != nil {
		return s.state.Save(context.Background(), "calls", calls)
	}
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(calls, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func normalizeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "success", "succeeded", "ok":
		return "success"
	case "failed", "error":
		return "failed"
	case "canceled", "cancelled":
		return "canceled"
	case "rejected", "denied", "blocked":
		return "rejected"
	case "running", "queued":
		return "running"
	default:
		return "other"
	}
}

func callCategory(call Call) string {
	if isContentPolicyError(call.Error) {
		return "rejected"
	}
	return normalizeStatus(call.Status)
}

func isContentPolicyError(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	return strings.Contains(lower, "content policy violation") ||
		strings.Contains(value, "防护限制") ||
		strings.Contains(value, "可能违反")
}

type errorReasonCount struct {
	Code     string
	Label    string
	Category string
	Value    int
}

func callErrorInfo(call Call) errorinfo.Info {
	if strings.TrimSpace(call.ErrorCode) == "" {
		return errorinfo.ClassifyText(call.Error, call.StatusCode)
	}
	classified := errorinfo.ClassifyText(call.Error, call.StatusCode)
	classified.Code = strings.TrimSpace(call.ErrorCode)
	if value := strings.TrimSpace(call.ErrorTitle); value != "" {
		classified.Title = value
	}
	if value := strings.TrimSpace(call.ErrorCategory); value != "" {
		classified.Category = value
	}
	if value := strings.TrimSpace(call.ErrorAction); value != "" {
		classified.Action = value
	}
	if value := strings.TrimSpace(call.ErrorHint); value != "" {
		classified.Hint = value
	}
	classified.Retryable = call.ErrorRetryable
	return classified
}

func recentFailedCall(call Call, classified errorinfo.Info) map[string]any {
	return map[string]any{
		"id": call.ID, "time": call.Time, "summary": call.Summary,
		"endpoint": call.Endpoint, "model": call.Model, "error": classified.Message,
		"error_code": classified.Code, "error_title": classified.Title,
		"error_category": classified.Category, "error_category_label": errorinfo.CategoryLabel(classified.Category),
		"retryable": classified.Retryable, "action": classified.Action, "hint": classified.Hint,
	}
}

func sortedErrorReasons(counts map[string]errorReasonCount) []map[string]any {
	items := make([]errorReasonCount, 0, len(counts))
	for _, item := range counts {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Value != items[j].Value {
			return items[i].Value > items[j].Value
		}
		return items[i].Code < items[j].Code
	})
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"code": item.Code, "label": item.Label, "category": item.Category,
			"category_label": errorinfo.CategoryLabel(item.Category), "value": item.Value,
		})
	}
	return out
}

func sortedCounts(counts map[string]int, key string) []map[string]any {
	type pair struct {
		name  string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for name, count := range counts {
		pairs = append(pairs, pair{name, count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].name < pairs[j].name
	})
	out := make([]map[string]any, 0, len(pairs))
	for _, item := range pairs {
		out = append(out, map[string]any{key: item.name, "value": item.count})
	}
	return out
}

type callMetaKey struct{}

type CallMeta struct {
	mu       sync.RWMutex
	Endpoint string
	model    string
}

func NewCallMeta(endpoint string) *CallMeta { return &CallMeta{Endpoint: endpoint} }

func WithCallMeta(ctx context.Context, meta *CallMeta) context.Context {
	return context.WithValue(ctx, callMetaKey{}, meta)
}

func SetModel(ctx context.Context, model string) {
	meta, _ := ctx.Value(callMetaKey{}).(*CallMeta)
	if meta == nil {
		return
	}
	meta.mu.Lock()
	meta.model = strings.TrimSpace(model)
	meta.mu.Unlock()
}

func MetaValues(ctx context.Context) (endpoint, model string) {
	meta, _ := ctx.Value(callMetaKey{}).(*CallMeta)
	if meta == nil {
		return "", ""
	}
	meta.mu.RLock()
	defer meta.mu.RUnlock()
	return meta.Endpoint, meta.model
}
