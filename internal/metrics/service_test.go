package metrics

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSummaryAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.json")
	svc := NewService(path)
	now := time.Date(2026, 7, 11, 12, 30, 30, 0, time.Local)
	svc.now = func() time.Time { return now }
	svc.Record(Call{Endpoint: "/v1/images/generations", Model: "gpt-image-2", StatusCode: 200})
	svc.Record(Call{Endpoint: "/v1/images/generations", Model: "gpt-image-2", StatusCode: 502, Error: "upstream failed"})
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
