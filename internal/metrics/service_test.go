package metrics

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"imagepool/internal/persistence"
)

type blockingMetricStore struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	calls   []Call
}

func (s *blockingMetricStore) Load(context.Context, string, any) error {
	return persistence.ErrNotFound
}

func (s *blockingMetricStore) Save(_ context.Context, _ string, value any) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-s.release
	items, _ := value.([]Call)
	s.mu.Lock()
	s.calls = append([]Call(nil), items...)
	s.mu.Unlock()
	return nil
}

func (s *blockingMetricStore) Delete(context.Context, string) error { return nil }

func (s *blockingMetricStore) Health(context.Context) (persistence.Health, error) {
	return persistence.Health{}, nil
}

func (s *blockingMetricStore) Close() {}

func TestSummaryAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.json")
	svc := NewService(path)
	defer svc.Close()
	now := time.Date(2026, 7, 11, 12, 30, 30, 0, time.Local)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Endpoint: "/v1/images/generations", Model: "gpt-image-2", StatusCode: 200})
	svc.Record(Call{Endpoint: "/v1/images/generations", Model: "gpt-image-2", StatusCode: 502, Error: "upstream failed"})
	if err := svc.Flush(); err != nil {
		t.Fatal(err)
	}
	summary := svc.Summary(time.Hour)
	runtime := summary["runtime"].(map[string]any)
	totals := runtime["totals"].(map[string]int)
	if totals["success"] != 1 || totals["failed"] != 1 {
		t.Fatalf("totals=%#v", totals)
	}
	if len(runtime["series"].([]map[string]any)) != 60 {
		t.Fatalf("series=%#v", runtime["series"])
	}
	if got := NewService(path).List("", "", ""); len(got) != 2 {
		t.Fatalf("persisted calls=%#v", got)
	}
}

func TestSummaryAggregatesLongRuntimeWindow(t *testing.T) {
	svc := NewService("")
	now := time.Date(2026, 7, 11, 12, 30, 30, 0, time.Local)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Time: now.Add(-2 * time.Hour), Endpoint: "/v1/images/generations", Status: "success"})
	svc.Record(Call{Time: now.Add(-48 * time.Hour), Endpoint: "/v1/images/generations", Status: "failed", Error: "upstream failed"})

	runtime := svc.Summary(30 * 24 * time.Hour)["runtime"].(map[string]any)
	if runtime["window_minutes"] != 30*24*60 || runtime["bucket_minutes"] != 24*60 {
		t.Fatalf("runtime=%#v", runtime)
	}
	if series := runtime["series"].([]map[string]any); len(series) != 30 {
		t.Fatalf("series length=%d", len(series))
	}
	totals := runtime["totals"].(map[string]int)
	if totals["success"] != 1 || totals["failed"] != 1 {
		t.Fatalf("totals=%#v", totals)
	}
}

func TestTodaySummaryUsesLocalDayAndExcludesRejectedFromFailureRate(t *testing.T) {
	svc := NewService("")
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.Local)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Time: now.Add(-13 * time.Hour), Endpoint: "/v1/images/generations", Model: "gpt-image-2", Status: "success"})
	svc.Record(Call{Time: now.Add(-2 * time.Hour), Endpoint: "/v1/images/generations", Model: "gpt-image-2", Status: "success"})
	svc.Record(Call{Time: now.Add(-time.Hour), Endpoint: "/v1/images/generations", Model: "gpt-image-2", Status: "failed", Error: "upstream failed"})
	svc.Record(Call{Time: now.Add(-30 * time.Minute), Endpoint: "/v1/images/edits", Model: "gpt-image-2", Status: "failed", Error: "content policy violation: 非常抱歉，生成的图片可能违反了关于裸露、色情或情色内容的防护限制。"})
	svc.Record(Call{Time: now.Add(-10 * time.Minute), Endpoint: "/v1/images/edits", Model: "gpt-image-2", Status: "canceled"})
	svc.Record(Call{Time: now.Add(time.Minute), Endpoint: "/v1/images/generations", Model: "gpt-image-2", Status: "success"})

	today := svc.TodaySummary()
	if today["date"] != now.In(time.Local).Format("2006-01-02") || today["total"] != 4 || today["availability_total"] != 2 {
		t.Fatalf("today=%#v", today)
	}
	if today["success_rate"] != float64(50) || today["error_rate"] != float64(50) {
		t.Fatalf("today=%#v", today)
	}
	totals := today["totals"].(map[string]int)
	if totals["success"] != 1 || totals["failed"] != 1 || totals["rejected"] != 1 || totals["canceled"] != 1 {
		t.Fatalf("totals=%#v", totals)
	}
	byStatus := today["by_status"].(map[string]int)
	if byStatus["failed"] != 1 || byStatus["rejected"] != 1 {
		t.Fatalf("byStatus=%#v", byStatus)
	}
	byEndpoint := today["by_endpoint"].(map[string]int)
	if byEndpoint["/v1/images/generations"] != 2 || byEndpoint["/v1/images/edits"] != 2 {
		t.Fatalf("byEndpoint=%#v", byEndpoint)
	}
	if failed := today["recent_failed"].([]map[string]any); len(failed) != 1 || failed[0]["error_code"] != "upstream_service_error" || failed[0]["error"] != "上游服务暂时异常，请稍后重试。" {
		t.Fatalf("recent_failed=%#v", failed)
	}
}

func TestSummaryAggregatesDynamicErrorsByStableCode(t *testing.T) {
	svc := NewService("")
	now := time.Date(2026, 7, 22, 16, 0, 0, 0, time.Local)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Time: now.Add(-2 * time.Second), Endpoint: "/v1/images/generations", Status: "failed", StatusCode: 429, Error: "image poll timeout: ChatGPT 生图任务已等待 61 秒"})
	svc.Record(Call{Time: now.Add(-time.Second), Endpoint: "/v1/images/generations", Status: "failed", StatusCode: 429, Error: "image poll timeout: ChatGPT 生图任务已等待 299 秒"})

	runtime := svc.Summary(time.Hour)["runtime"].(map[string]any)
	reasons := runtime["error_reasons"].([]map[string]any)
	if len(reasons) != 1 || reasons[0]["code"] != "oai_image_generation_timeout" || reasons[0]["value"] != 2 {
		t.Fatalf("error_reasons=%#v", reasons)
	}
}

func TestDelete(t *testing.T) {
	svc := NewService("")
	svc.Record(Call{ID: "a", Endpoint: "/v1/search", Status: "success"})
	svc.Record(Call{ID: "b", Endpoint: "/v1/search", Status: "failed"})
	if removed := svc.Delete([]string{"a"}); removed != 1 || len(svc.List("", "", "")) != 1 {
		t.Fatalf("removed=%d calls=%#v", removed, svc.List("", "", ""))
	}
}

func TestStabilityUsesRollingSixtySecondWindow(t *testing.T) {
	svc := NewService("")
	now := time.Date(2026, 7, 12, 14, 10, 30, 500000000, time.UTC)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Time: now.Add(-10 * time.Second), Endpoint: "/v1/images/generations", Status: "success"})
	svc.Record(Call{Time: now.Add(-20 * time.Second), Endpoint: "/v1/images/generations", Status: "failed"})
	svc.Record(Call{Time: now.Add(-30 * time.Second), Endpoint: "/v1/images/generations", Status: "failed", Error: "content policy violation: 非常抱歉，生成的图片可能违反了关于裸露、色情或情色内容的防护限制。"})
	svc.Record(Call{Time: now.Add(-59900 * time.Millisecond), Endpoint: "/v1/images/generations", Status: "success"})
	svc.Record(Call{Time: now.Add(-70 * time.Second), Endpoint: "/v1/images/generations", Status: "failed"})

	stability := svc.Stability(time.Minute)
	if stability.WindowSeconds != 60 || stability.Total != 3 || stability.Success != 2 || stability.Failed != 1 {
		t.Fatalf("stability=%#v", stability)
	}
	if stability.StabilityPercent != 200.0/3 || stability.Status != "degraded" || len(stability.Series) != 60 {
		t.Fatalf("stability=%#v", stability)
	}
}

func TestRecentStatsUsesLastCallsAndExcludesRejectedFromFailureRate(t *testing.T) {
	svc := NewService("")
	now := time.Date(2026, 7, 14, 11, 30, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Time: now.Add(-5 * time.Second), Endpoint: "/v1/images/generations", Status: "success", DurationMS: 1000})
	svc.Record(Call{Time: now.Add(-4 * time.Second), Endpoint: "/v1/images/generations", Status: "failed", DurationMS: 3000})
	svc.Record(Call{Time: now.Add(-3 * time.Second), Endpoint: "/v1/images/generations", Status: "failed", DurationMS: 500, Error: "content policy violation: 非常抱歉，生成的图片可能违反了关于裸露、色情或情色内容的防护限制。"})
	svc.Record(Call{Time: now.Add(-2 * time.Second), Endpoint: "/v1/images/generations", Status: "canceled", DurationMS: 700})

	recent := svc.RecentStats(60)
	if recent.Total != 4 || recent.AvailabilityTotal != 2 || recent.Success != 1 || recent.Failed != 1 || recent.Rejected != 1 || recent.Canceled != 1 {
		t.Fatalf("recent=%#v", recent)
	}
	if recent.SuccessRate != 50 || recent.FailureRate != 50 || recent.AverageDurationMS != 2000 || recent.AverageSuccessDurationMS != 1000 || recent.AverageFailureDurationMS != 3000 {
		t.Fatalf("recent=%#v", recent)
	}
}

func TestSummaryExcludesCanceledAndRejectedFromAvailabilityRate(t *testing.T) {
	svc := NewService("")
	now := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "success"})
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "failed"})
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "failed", Error: "content policy violation: 非常抱歉，生成的图片可能违反了关于裸露、色情或情色内容的防护限制。"})
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "canceled"})
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "rejected"})

	runtime := svc.Summary(time.Hour)["runtime"].(map[string]any)
	totals := runtime["totals"].(map[string]int)
	if totals["failed"] != 1 || totals["canceled"] != 1 || totals["rejected"] != 2 || runtime["success_rate"] != float64(50) || runtime["error_rate"] != float64(50) {
		t.Fatalf("runtime=%#v", runtime)
	}
}

func TestRecordDoesNotBlockOnPersistence(t *testing.T) {
	state := &blockingMetricStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	svc := NewServiceWithPersistence(state)
	released := false
	defer func() {
		if !released {
			close(state.release)
		}
		svc.Close()
	}()

	svc.Record(Call{Endpoint: "/v1/images/generations", Status: "success"})
	select {
	case <-state.started:
	case <-time.After(time.Second):
		t.Fatal("metrics persistence worker did not start")
	}

	started := time.Now()
	for index := 0; index < 50; index++ {
		svc.Record(Call{Endpoint: "/v1/images/generations", Status: "success"})
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("50 metric records blocked on persistence for %s", elapsed)
	}
	if got := len(svc.List("", "", "")); got != 51 {
		t.Fatalf("in-memory records=%d", got)
	}

	released = true
	close(state.release)
	svc.Close()
	state.mu.Lock()
	persisted := len(state.calls)
	state.mu.Unlock()
	if persisted != 51 {
		t.Fatalf("persisted records=%d", persisted)
	}
}
