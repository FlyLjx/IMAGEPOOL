package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"imagepool/internal/config"
)

func TestBarkSendsConfiguredPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/push" || r.Method != http.MethodPost {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["device_key"] != "key" || payload["title"] != "IMAGE POOL - Bark test" {
			t.Fatalf("payload=%#v", payload)
		}
		_, _ = w.Write([]byte(`{"code":200}`))
	}))
	defer srv.Close()
	result := TestBark(context.Background(), config.BarkNotification{Enabled: true, ServerURL: srv.URL, DeviceKey: "key", TitlePrefix: "IMAGE POOL", Level: "active"}, srv.Client())
	if !result.OK || result.Status != http.StatusOK {
		t.Fatalf("result=%#v", result)
	}
}

func TestBarkRequiresEnabledDeviceKey(t *testing.T) {
	result := TestBark(context.Background(), config.BarkNotification{}, nil)
	if result.OK || result.Error == "" {
		t.Fatalf("result=%#v", result)
	}
}
