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

func TestSummaryExcludesCanceledAndRejectedFromAvailabilityRate(t *testing.T) {
	svc := NewService("")
	now := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "success"})
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "failed"})
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "canceled"})
	svc.Record(Call{Time: now, Endpoint: "/v1/images/generations", Status: "rejected"})

	runtime := svc.Summary(time.Hour)["runtime"].(map[string]any)
	totals := runtime["totals"].(map[string]int)
	if totals["canceled"] != 1 || totals["rejected"] != 1 || runtime["success_rate"] != float64(50) || runtime["error_rate"] != float64(50) {
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
