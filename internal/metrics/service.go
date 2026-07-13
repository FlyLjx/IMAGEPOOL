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

	"imagepool/internal/persistence"
)

const defaultMaxEntries = 5000

type Call struct {
	ID         string         `json:"id"`
	Time       time.Time      `json:"time"`
	Endpoint   string         `json:"endpoint"`
	Model      string         `json:"model,omitempty"`
	Status     string         `json:"status"`
	StatusCode int            `json:"status_code"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Error      string         `json:"error,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Details    map[string]any `json:"detail,omitempty"`
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
}

type Service struct {
	mu       sync.RWMutex
	path     string
	state    persistence.Store
	max      int
	calls    []Call
	now      func() time.Time
	sequence uint64
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
	return s
}

func (s *Service) Record(call Call) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	_ = s.saveLocked()
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
	defer s.mu.Unlock()
	if len(wanted) == 0 {
		removed := len(s.calls)
		s.calls = nil
		_ = s.saveLocked()
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
		_ = s.saveLocked()
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
	byReason := map[string]int{}
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
		category := normalizeStatus(call.Status)
		totals[category]++
		bucketTime := runtimeBucketTime(call.Time, bucket)
		bucketKey := bucketTime.Format(time.RFC3339)
		if bucketCounts[bucketKey] == nil {
			bucketCounts[bucketKey] = map[string]int{}
		}
		bucketCounts[bucketKey][category]++
		if category == "failed" {
			reason := failureReason(call.Error)
			if reason != "" {
				byReason[reason]++
			}
			if len(recentFailed) < 20 {
				recentFailed = append(recentFailed, map[string]any{"id": call.ID, "time": call.Time, "summary": call.Summary, "endpoint": call.Endpoint, "model": call.Model, "error": call.Error})
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
	reasons := sortedCounts(byReason, "label")
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
	}
	for _, call := range s.List("", "", "") {
		if call.Time.Before(start) || call.Time.After(now) {
			continue
		}
		bucket := call.Time.Truncate(time.Second)
		index, inSeries := bySecond[bucket]
		switch normalizeStatus(call.Status) {
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

func (s *Service) saveLocked() error {
	if s.state != nil {
		return s.state.Save(context.Background(), "calls", s.calls)
	}
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.calls, "", "  ")
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

func failureReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if len(value) > 120 {
		return value[:120]
	}
	return value
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
