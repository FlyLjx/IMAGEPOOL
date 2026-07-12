package tasks

import (
	"context"
	"testing"
	"time"

	"imagepool/internal/images"
	"imagepool/internal/openaiweb"
)

type taskSvc struct{ ch chan struct{} }

type queuedTaskSvc struct {
	waiting chan struct{}
	release chan struct{}
}

func (s queuedTaskSvc) Generate(ctx context.Context, req images.Request) (images.Response, error) {
	if req.Progress != nil {
		req.Progress(openaiweb.ProgressEvent{Progress: "waiting_account", Message: "暂无空闲账号，任务排队等待"})
	}
	close(s.waiting)
	select {
	case <-s.release:
	case <-ctx.Done():
		return images.Response{}, ctx.Err()
	}
	if req.Progress != nil {
		req.Progress(openaiweb.ProgressEvent{Progress: "checking_account", Message: "验证账号 Token"})
	}
	return images.Response{Data: []images.Data{{URL: "u"}}}, nil
}

func (s queuedTaskSvc) GenerateWithAccount(ctx context.Context, _ string, req images.Request) (images.Response, error) {
	return s.Generate(ctx, req)
}

func (s taskSvc) Generate(ctx context.Context, req images.Request) (images.Response, error) {
	if s.ch != nil {
		close(s.ch)
	}
	return images.Response{Data: []images.Data{{URL: "u"}}}, nil
}

func (s taskSvc) GenerateWithAccount(ctx context.Context, _ string, req images.Request) (images.Response, error) {
	return s.Generate(ctx, req)
}

func TestSubmitCreatesUniqueTasksNoReuse(t *testing.T) {
	m := NewManager(taskSvc{})
	a := m.SubmitGeneration("same-client-id", images.Request{Prompt: "a"})
	b := m.SubmitGeneration("same-client-id", images.Request{Prompt: "a"})
	if a.ID == b.ID {
		t.Fatal("duplicate client id reused task")
	}
}

func TestTaskLifecycle(t *testing.T) {
	ch := make(chan struct{})
	m := NewManager(taskSvc{ch: ch})
	task := m.SubmitGeneration("c", images.Request{Prompt: "a"})
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("service not called")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, ok := m.Status(task.ID)
		if ok && got.Status == StatusSucceeded {
			if got.ProgressPercent != 100 || len(got.Data) != 1 || got.StatusLogCount == 0 {
				t.Fatalf("bad completed task: %#v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := m.Status(task.ID)
	t.Fatalf("not succeeded: %#v", got)
}

func TestTaskRemainsQueuedWhileWaitingForAnAccount(t *testing.T) {
	svc := queuedTaskSvc{waiting: make(chan struct{}), release: make(chan struct{})}
	m := NewManager(svc)
	task := m.SubmitGeneration("queued", images.Request{Prompt: "a"})
	select {
	case <-svc.waiting:
	case <-time.After(time.Second):
		t.Fatal("task did not report account queueing")
	}
	queued, ok := m.Status(task.ID)
	if !ok || queued.Status != StatusQueued || queued.Progress != StatusQueued || queued.StartedAt != nil {
		t.Fatalf("queued task=%#v ok=%v", queued, ok)
	}

	close(svc.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		completed, ok := m.Status(task.ID)
		if ok && completed.Status == StatusSucceeded {
			if completed.StartedAt == nil {
				t.Fatalf("started timestamp missing: %#v", completed)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	completed, _ := m.Status(task.ID)
	t.Fatalf("task did not complete: %#v", completed)
}

func TestRunGenerationCreatesTrackedTask(t *testing.T) {
	m := NewManager(taskSvc{})
	task, result, err := m.RunGenerationForOwner(context.Background(), "user-a", images.Request{Prompt: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusSucceeded || task.OwnerID != "user-a" || len(task.Data) != 1 || len(result.Data) != 1 {
		t.Fatalf("task=%#v result=%#v", task, result)
	}
	stored, ok := m.StatusForOwner(task.ID, "user-a", false)
	if !ok || stored.Status != StatusSucceeded || stored.StatusLogCount == 0 {
		t.Fatalf("stored=%#v ok=%v", stored, ok)
	}
}

func TestTaskVisibilityIsScopedToOwner(t *testing.T) {
	m := NewManager(taskSvc{})
	a := m.SubmitGenerationForOwner("user-a", "a", images.Request{Prompt: "a"})
	b := m.SubmitGenerationForOwner("user-b", "b", images.Request{Prompt: "b"})
	items := m.ListForOwner(nil, "user-a", false)
	if len(items) != 1 || items[0].ID != a.ID {
		t.Fatalf("user-a tasks=%#v", items)
	}
	if _, ok := m.StatusForOwner(b.ID, "user-a", false); ok {
		t.Fatal("user-a can read user-b task")
	}
	if _, ok := m.CancelForOwner(b.ID, "user-a", false); ok {
		t.Fatal("user-a can cancel user-b task")
	}
}

func TestProgressPercentIncludesPrecheckQueueAndImagePolling(t *testing.T) {
	if got := progressPercent("waiting_account_precheck"); got != 10 {
		t.Fatalf("precheck queue progress=%d", got)
	}
	if got := progressPercent("polling_image"); got != 90 {
		t.Fatalf("polling progress=%d", got)
	}
}
