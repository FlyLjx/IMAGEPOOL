package openaiweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
)

func TestPollImageResultsRetriesFreshConversationInaccessible(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/conversation/conv-1" {
			http.NotFound(w, r)
			return
		}
		if hits.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"detail": map[string]any{"code": "conversation_inaccessible"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mapping": map[string]any{
				"message": map[string]any{
					"content": map[string]any{"parts": []any{"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	cfg.ImagePollTimeoutSecs = 1
	cfg.ImagePollIntervalSecs = 0
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithSleep(func(context.Context, time.Duration) error { return nil }))
	files, sediments, err := client.pollImageResults(context.Background(), accounts.Account{AccessToken: "token"}, "conv-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() < 2 || len(files) != 1 || files[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" || len(sediments) != 0 {
		t.Fatalf("hits=%d files=%#v sediments=%#v", hits.Load(), files, sediments)
	}
}

func TestPollImageResultsEmitsHeartbeatAndRetriesSlowPoll(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/conversation/conv-1" {
			http.NotFound(w, r)
			return
		}
		if hits.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(80 * time.Millisecond):
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mapping": map[string]any{
				"message": map[string]any{
					"content": map[string]any{"parts": []any{"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	cfg.ImagePollTimeoutSecs = 0.2
	cfg.ImagePollIntervalSecs = 0.001
	cfg.ImageSettleEnabled = false
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	client.pollHeartbeatInterval = time.Millisecond
	client.pollRequestTimeout = 10 * time.Millisecond
	events := []ProgressEvent{}
	files, sediments, err := client.pollImageResultsWithProgress(context.Background(), accounts.Account{AccessToken: "token"}, "conv-1", nil, nil, func(event ProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() < 2 || len(files) != 1 || len(sediments) != 0 {
		t.Fatalf("hits=%d files=%#v sediments=%#v", hits.Load(), files, sediments)
	}
	if len(events) == 0 || events[0].Progress != "polling_image" || events[0].Details["conversation_id"] != "conv-1" {
		t.Fatalf("events=%#v", events)
	}
}
