package updater

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTriggerCallsWatchtowerWithBearerToken(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/update" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization=%q", got)
		}
		called <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	service := New(server.URL+"/v1/update", "test-token")
	service.client = server.Client()
	service.delay = 0
	status, started, err := service.Trigger("v0.1.3")
	if err != nil || !started || !status.Updating || status.TargetVersion != "0.1.3" {
		t.Fatalf("status=%#v started=%v err=%v", status, started, err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("updater was not called")
	}
}

func TestTriggerRejectsInvalidVersion(t *testing.T) {
	service := New("http://example.test/v1/update", "")
	if _, _, err := service.Trigger("latest"); err != ErrInvalidVersion {
		t.Fatalf("err=%v", err)
	}
}

func TestTriggerRejectsPinnedImageTag(t *testing.T) {
	service := NewWithImage("http://example.test/v1/update", "", "ghcr.io/flyljx/imagepool", "0.1.29")
	status, started, err := service.Trigger("0.1.30")
	if err != ErrPinnedImageTag || started {
		t.Fatalf("status=%#v started=%v err=%v", status, started, err)
	}
	if status.UpdateMode != "pinned" || status.ImageTag != "0.1.29" || status.LastError == "" {
		t.Fatalf("unexpected status=%#v", status)
	}
}

func TestTriggerAllowsLatestImageTag(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	service := NewWithImage(server.URL+"/v1/update", "", "ghcr.io/flyljx/imagepool", "latest")
	service.client = server.Client()
	service.delay = 0
	status, started, err := service.Trigger("0.1.30")
	if err != nil || !started || status.UpdateMode != "watchtower_latest" {
		t.Fatalf("status=%#v started=%v err=%v", status, started, err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("updater was not called")
	}
}
