package tasks

import (
	"context"
	"testing"
	"time"

	"imagepool/internal/images"
	"imagepool/internal/openaiweb"
	"imagepool/internal/persistence"
)

type taskSvc struct{ ch chan struct{} }

type queuedTaskSvc struct {
	waiting chan struct{}
	release chan struct{}
}

type blockingCollectionStore struct {
	saveStarted chan struct{}
	release     chan struct{}
	items       map[string]any
}

func (s *blockingCollectionStore) Load(context.Context, string, any) error {
	return persistence.ErrNotFound
}

func (s *blockingCollectionStore) Save(context.Context, string, any) error { return nil }
func (s *blockingCollectionStore) Delete(context.Context, string) error    { return nil }
func (s *blockingCollectionStore) Health(context.Context) (persistence.Health, error) {
	return persistence.Health{}, nil
}
func (s *blockingCollectionStore) Close() {}
func (s *blockingCollectionStore) LoadCollection(context.Context, string, any) error {
	return persistence.ErrNotFound
}
func (s *blockingCollectionStore) SaveCollectionItems(_ context.Context, _ string, items map[string]any) error {
	select {
	case s.saveStarted <- struct{}{}:
	default:
	}
	<-s.release
	for id, item := range items {
		s.items[id] = item
	}
	return nil
}
func (s *blockingCollectionStore) DeleteCollection(context.Context, string) error { return nil }

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

func TestTaskPersistenceNeverBlocksSubmissionOrStatus(t *testing.T) {
	state := &blockingCollectionStore{saveStarted: make(chan struct{}, 1), release: make(chan struct{}), items: map[string]any{}}
	m := NewManagerWithPersistence(taskSvc{}, state)
	released := false
	defer func() {
		if !released {
			close(state.release)
			m.Close()
		}
	}()
	first := m.SubmitGeneration("first", images.Request{Prompt: "first"})
	select {
	case <-state.saveStarted:
	case <-time.After(time.Second):
		t.Fatal("persistence worker did not start")
	}

	started := time.Now()
	submitted := []Task{first}
	for index := 0; index < 50; index++ {
		submitted = append(submitted, m.SubmitGeneration("concurrent", images.Request{Prompt: "concurrent"}))
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("50 submissions blocked on persistence for %s", elapsed)
	}
	started = time.Now()
	if _, ok := m.Status(first.ID); !ok {
		t.Fatal("first task missing")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("status blocked on persistence for %s", elapsed)
	}

	released = true
	close(state.release)
	m.Close()
	if _, ok := state.items[first.ID]; !ok {
		t.Fatalf("first task was not persisted: %#v", state.items)
	}
	for _, task := range submitted {
		if _, ok := state.items[task.ID]; !ok {
			t.Fatalf("task %s was not persisted", task.ID)
		}
	}
}
