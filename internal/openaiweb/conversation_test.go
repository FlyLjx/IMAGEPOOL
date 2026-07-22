package openaiweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepool/internal/accounts"
	"imagepool/internal/config"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

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

func TestPollImageResultsFiltersUploadedReferenceWithoutGeneratedRole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/conversation/conv-1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mapping": map[string]any{
				"message": map[string]any{
					"message": map[string]any{
						"content": map[string]any{"parts": []any{
							map[string]any{"asset_pointer": "file-service://file_uploaded"},
							map[string]any{"asset_pointer": "file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"},
						}},
					},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	cfg.ImageSettleEnabled = false
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	files, sediments, err := client.pollImageResults(context.Background(), accounts.Account{AccessToken: "token"}, "conv-1", nil, nil, map[string]bool{"file_uploaded": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" || len(sediments) != 0 {
		t.Fatalf("files=%#v sediments=%#v", files, sediments)
	}
}

func TestPollImageResultsFiltersUploadedSedimentReference(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/conversation/conv-1" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mapping": map[string]any{
				"message": map[string]any{
					"message": map[string]any{
						"content": map[string]any{"parts": []any{
							"sediment://file_uploaded",
							map[string]any{"asset_pointer": "file-service://file_uploaded"},
							map[string]any{"asset_pointer": "file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"},
						}},
					},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	cfg.ImageSettleEnabled = false
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	files, sediments, err := client.pollImageResults(context.Background(), accounts.Account{AccessToken: "token"}, "conv-1", nil, nil, map[string]bool{"file_uploaded": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" || len(sediments) != 0 {
		t.Fatalf("files=%#v sediments=%#v", files, sediments)
	}
}

func TestStartImageGenerationReadsImageReferenceAfterConversationID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/f/conversation" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"message\":{\"author\":{\"role\":\"tool\"},\"content\":{\"parts\":[{\"asset_pointer\":\"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa\"}]}}}\n\n"))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	conversationID, fileIDs, sedimentIDs, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "gpt-image-2", chatRequirements{}, "conduit", "trace", "parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if conversationID != "conv-1" || len(fileIDs) != 1 || fileIDs[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" || len(sedimentIDs) != 0 {
		t.Fatalf("conversation=%q files=%#v sediments=%#v", conversationID, fileIDs, sedimentIDs)
	}
}

func TestStartImageGenerationParsesMultilineSSEEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/f/conversation" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": keepalive\r\nevent: message\r\ndata: {\"conversation_id\":\"conv-1\",\"message\":{\"author\":{\"role\":\"tool\"},\r\ndata: \"content\":{\"parts\":[{\"asset_pointer\":\"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa\"}]}}}\r\n\r\n"))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	conversationID, fileIDs, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "gpt-image-2", chatRequirements{}, "conduit", "trace", "parent", nil)
	if err != nil || conversationID != "conv-1" || len(fileIDs) != 1 || fileIDs[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("conversation=%q files=%#v err=%v", conversationID, fileIDs, err)
	}
}

func TestStartImageGenerationDoesNotSendUnsupportedOutputFormatMetadata(t *testing.T) {
	seenOutputFormat := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/f/conversation" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if messages, _ := body["messages"].([]any); len(messages) > 0 {
			if message, _ := messages[0].(map[string]any); message != nil {
				if metadata, _ := message["metadata"].(map[string]any); metadata != nil {
					_, seenOutputFormat = metadata["output_format"]
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\"}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	_, _, _, err := client.startImageGenerationWithinBudget(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "gpt-image-2", chatRequirements{}, "conduit", "trace", "parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if seenOutputFormat {
		t.Fatal("ChatGPT Web conversation payload must not send output_format metadata")
	}
}

func TestStartImageGenerationResumesIdleToolStream(t *testing.T) {
	var resumeSeen atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/f/conversation":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["parent_message_id"] != "parent-stable" {
				t.Errorf("start parent_message_id=%#v", body["parent_message_id"])
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"resume_conversation_token\",\"token\":\"resume-token\",\"conversation_id\":\"conv-1\"}\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		case "/backend-api/f/conversation/resume":
			resumeSeen.Store(true)
			if r.Header.Get("X-Conduit-Token") != "resume-token" || r.Header.Get("X-Oai-Turn-Trace-Id") != "trace" {
				t.Errorf("bad resume headers: %v", r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["conversation_id"] != "conv-1" || body["offset"] != float64(1) {
				t.Errorf("bad resume payload: %#v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\",\"message\":{\"author\":{\"role\":\"tool\"},\"content\":{\"parts\":[{\"asset_pointer\":\"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa\"}]}}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	client.imageStreamIdleTimeout = 100 * time.Millisecond
	conversationID, fileIDs, sedimentIDs, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent-stable", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !resumeSeen.Load() || conversationID != "conv-1" || len(fileIDs) != 1 || fileIDs[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" || len(sedimentIDs) != 0 {
		t.Fatalf("resumed=%t conversation=%q files=%#v sediments=%#v", resumeSeen.Load(), conversationID, fileIDs, sedimentIDs)
	}
}

func TestStartImageGenerationKeepsCommentHeartbeatStreamOpen(t *testing.T) {
	var resumeCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"resume_conversation_token\",\"token\":\"resume-token\",\"conversation_id\":\"conv-1\"}\n\n"))
			flusher, _ := w.(http.Flusher)
			if flusher != nil {
				flusher.Flush()
			}
			for index := 0; index < 6; index++ {
				time.Sleep(10 * time.Millisecond)
				_, _ = w.Write([]byte(": keepalive\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
			}
			_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\",\"message\":{\"author\":{\"role\":\"tool\"},\"content\":{\"parts\":[{\"asset_pointer\":\"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa\"}]}}}\n\n"))
		case "/backend-api/f/conversation/resume":
			resumeCalls.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	client.imageStreamOpenTimeout = 15 * time.Millisecond
	client.imageStreamIdleTimeout = 45 * time.Millisecond
	conversationID, fileIDs, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumeCalls.Load() != 0 || conversationID != "conv-1" || len(fileIDs) != 1 || fileIDs[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("resumes=%d conversation=%q files=%#v", resumeCalls.Load(), conversationID, fileIDs)
	}
}

func TestConsumeImageStreamStopsAfterGenerationBudgetDespiteHeartbeats(t *testing.T) {
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := writer.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
		}
	}()

	client := NewClient(config.Default())
	client.pollTimeout = 30 * time.Millisecond
	client.imageStreamIdleTimeout = time.Second
	_, _, err := client.consumeImageStream(context.Background(), reader, nil, &imageStreamState{})
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat writer was not released")
	}
}

func TestConsumeImageStreamFallsBackToPollingAfterReferenceStall(t *testing.T) {
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		if _, err := writer.Write([]byte("data: {\"conversation_id\":\"conv-1\"}\n\n")); err != nil {
			return
		}
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := writer.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
		}
	}()

	client := NewClient(config.Default())
	client.pollTimeout = time.Second
	client.imageStreamIdleTimeout = time.Second
	client.imageStreamReferenceTimeout = 25 * time.Millisecond
	state := &imageStreamState{}
	found, streamDone, err := client.consumeImageStream(context.Background(), reader, nil, state)
	if err != nil || found || !streamDone || state.conversationID != "conv-1" {
		t.Fatalf("found=%t done=%t conversation=%q err=%v", found, streamDone, state.conversationID, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat writer was not released")
	}
}

func TestConsumeImageStreamReturnsEarlyTimeoutWithoutConversationID(t *testing.T) {
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := writer.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
		}
	}()

	client := NewClient(config.Default())
	client.pollTimeout = time.Second
	client.imageStreamIdleTimeout = time.Second
	client.imageStreamReferenceTimeout = 25 * time.Millisecond
	started := time.Now()
	_, _, err := client.consumeImageStream(context.Background(), reader, nil, &imageStreamState{})
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("reference stall did not return early: %s", elapsed)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat writer was not released")
	}
}

func TestConsumeImageStreamFiltersUploadedReferenceWithoutRole(t *testing.T) {
	payload := `data: {"conversation_id":"conv-1","message":{"content":{"parts":[{"asset_pointer":"file-service://file_uploaded"},{"asset_pointer":"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}]}}}` + "\n\n"
	client := NewClient(config.Default())
	state := &imageStreamState{}
	found, streamDone, err := client.consumeImageStream(context.Background(), io.NopCloser(strings.NewReader(payload)), []uploadMeta{{FileID: "file_uploaded"}}, state)
	if err != nil || !found || streamDone {
		t.Fatalf("found=%t done=%t err=%v", found, streamDone, err)
	}
	if state.conversationID != "conv-1" || len(state.fileIDs) != 1 || state.fileIDs[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("conversation=%q files=%#v", state.conversationID, state.fileIDs)
	}
}

func TestConsumeImageStreamFiltersUploadedSedimentReference(t *testing.T) {
	payload := `data: {"conversation_id":"conv-1","message":{"content":{"parts":["sediment://file_uploaded",{"asset_pointer":"file-service://file_uploaded"}]}}}` + "\n\n" +
		"data: [DONE]\n\n"
	client := NewClient(config.Default())
	state := &imageStreamState{}
	found, streamDone, err := client.consumeImageStream(context.Background(), io.NopCloser(strings.NewReader(payload)), []uploadMeta{{FileID: "file_uploaded"}}, state)
	if err != nil || found || !streamDone {
		t.Fatalf("found=%t done=%t err=%v", found, streamDone, err)
	}
	if state.conversationID != "conv-1" || len(state.fileIDs) != 0 || len(state.sedimentIDs) != 0 {
		t.Fatalf("conversation=%q files=%#v sediments=%#v", state.conversationID, state.fileIDs, state.sedimentIDs)
	}
}

func TestImageGenerationErrorReportsActualElapsedBudget(t *testing.T) {
	ctx := context.WithValue(context.Background(), imageGenerationStartedAtKey{}, time.Now().Add(-42*time.Second))
	err := imageGenerationError(ctx, context.DeadlineExceeded)
	if !errors.Is(err, ErrPollTimeout) || !strings.Contains(err.Error(), "已等待 42 秒") {
		t.Fatalf("err=%v", err)
	}
}

func TestStartImageGenerationRetriesOpeningStreamThenReturnsTimeout(t *testing.T) {
	var calls atomic.Int32
	var canceled atomic.Int32
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/backend-api/f/conversation" {
			return nil, fmt.Errorf("path=%s", request.URL.Path)
		}
		calls.Add(1)
		<-request.Context().Done()
		canceled.Add(1)
		return nil, request.Context().Err()
	})}

	cfg := config.Default()
	cfg.ChatGPTBaseURL = "http://stream.test"
	client := NewClient(cfg, WithHTTPClient(httpClient), WithSleep(func(context.Context, time.Duration) error { return nil }))
	client.imageStreamOpenTimeout = 15 * time.Millisecond
	_, _, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	if !errors.Is(err, errImageStreamOpenTimeout) {
		t.Fatalf("err=%v", err)
	}
	if calls.Load() != maxImageStartAttempts || canceled.Load() != maxImageStartAttempts {
		t.Fatalf("opens=%d canceled=%d", calls.Load(), canceled.Load())
	}
}

func TestStartImageGenerationBoundsAllOpenAttemptsByGenerationBudget(t *testing.T) {
	var calls atomic.Int32
	client := NewClient(config.Default(), WithHTTPClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}), WithSleep(func(context.Context, time.Duration) error { return nil }))
	client.pollTimeout = 30 * time.Millisecond
	client.imageStreamOpenTimeout = time.Second
	started := time.Now()
	_, _, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("generation budget was ignored for %s", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("open attempts=%d", calls.Load())
	}
}

func TestStartImageGenerationReturnsTimeoutAfterResumeOpenTimeouts(t *testing.T) {
	var resumeCalls atomic.Int32
	var canceled atomic.Int32
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/backend-api/f/conversation":
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"resume_conversation_token\",\"token\":\"resume-token\",\"conversation_id\":\"conv-1\"}\n\n")), Request: request}, nil
		case "/backend-api/f/conversation/resume":
			resumeCalls.Add(1)
			<-request.Context().Done()
			canceled.Add(1)
			return nil, request.Context().Err()
		default:
			return nil, fmt.Errorf("path=%s", request.URL.Path)
		}
	})}

	cfg := config.Default()
	cfg.ChatGPTBaseURL = "http://stream.test"
	client := NewClient(cfg, WithHTTPClient(httpClient), WithSleep(func(context.Context, time.Duration) error { return nil }))
	client.imageStreamOpenTimeout = 15 * time.Millisecond
	_, _, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	if !errors.Is(err, errImageStreamOpenTimeout) {
		t.Fatalf("err=%v", err)
	}
	if resumeCalls.Load() != maxImageResumeAttempts || canceled.Load() != maxImageResumeAttempts {
		t.Fatalf("resumes=%d canceled=%d", resumeCalls.Load(), canceled.Load())
	}
}

func TestStartImageGenerationRetriesResumeRequestsUpToLimit(t *testing.T) {
	var resumeCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"resume_conversation_token\",\"token\":\"resume-token\",\"conversation_id\":\"conv-1\"}\n\n"))
		case "/backend-api/f/conversation/resume":
			if resumeCalls.Add(1) < maxImageResumeAttempts {
				http.Error(w, "temporary gateway error", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\",\"message\":{\"author\":{\"role\":\"tool\"},\"content\":{\"parts\":[{\"asset_pointer\":\"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa\"}]}}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithSleep(func(context.Context, time.Duration) error { return nil }))
	conversationID, fileIDs, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumeCalls.Load() != maxImageResumeAttempts || conversationID != "conv-1" || len(fileIDs) != 1 || fileIDs[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("resumes=%d conversation=%q files=%#v", resumeCalls.Load(), conversationID, fileIDs)
	}
}

func TestStartImageGenerationRetriesTransientStartError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/f/conversation" {
			http.NotFound(w, r)
			return
		}
		if hits.Add(1) == 1 {
			http.Error(w, "temporary gateway error", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\",\"message\":{\"author\":{\"role\":\"tool\"},\"content\":{\"parts\":[{\"asset_pointer\":\"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa\"}]}}}\n\n"))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithSleep(func(context.Context, time.Duration) error { return nil }))
	conversationID, fileIDs, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 || conversationID != "conv-1" || len(fileIDs) != 1 {
		t.Fatalf("hits=%d conversation=%q files=%#v", hits.Load(), conversationID, fileIDs)
	}
}

func TestStartImageGenerationReturnsInitialRateLimitWithoutSameAccountRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/f/conversation" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Retry-After", "30")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithSleep(func(context.Context, time.Duration) error { return nil }))
	_, _, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusTooManyRequests || upstream.RetryAfter != 30 {
		t.Fatalf("err=%#v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("same account start retries=%d", hits.Load())
	}
}

func TestStartImageGenerationRetriesAcceptedConversationAfterResumeRateLimit(t *testing.T) {
	var resumeHits atomic.Int32
	var retryDelay time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"resume_conversation_token\",\"token\":\"resume-token\",\"conversation_id\":\"conv-1\"}\n\n"))
		case "/backend-api/f/conversation/resume":
			if resumeHits.Add(1) == 1 {
				w.Header().Set("Retry-After", "30")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\",\"message\":{\"author\":{\"role\":\"tool\"},\"content\":{\"parts\":[{\"asset_pointer\":\"file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa\"}]}}}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithSleep(func(_ context.Context, delay time.Duration) error {
		retryDelay = delay
		return nil
	}))
	conversationID, fileIDs, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumeHits.Load() != 2 || retryDelay != 5*time.Second || conversationID != "conv-1" || len(fileIDs) != 1 {
		t.Fatalf("resume_hits=%d retry_delay=%s conversation=%q files=%#v", resumeHits.Load(), retryDelay, conversationID, fileIDs)
	}
}

func TestImageResumeRetryPolicy(t *testing.T) {
	if !isRetryableResumeError(errors.New("connection reset")) {
		t.Fatal("network errors must be retryable")
	}
	if isRetryableResumeError(context.Canceled) || isRetryableResumeError(context.DeadlineExceeded) {
		t.Fatal("context termination must not be retryable")
	}
	if isRetryableResumeError(ErrImageGenerationTerminated) {
		t.Fatal("terminal image-tool status must not resume the same conversation")
	}
	if !isRetryableResumeError(&UpstreamError{StatusCode: http.StatusBadGateway}) {
		t.Fatal("502 must be retryable")
	}
	if isRetryableResumeError(&UpstreamError{StatusCode: http.StatusInternalServerError}) {
		t.Fatal("unlisted HTTP errors must not be retryable")
	}
	if !isResumePollingFallback(&UpstreamError{StatusCode: http.StatusNotFound}) {
		t.Fatal("resume 404 must fall back to conversation polling")
	}
	if got := imageResumeRetryDelay(0); got != 300*time.Millisecond {
		t.Fatalf("first retry delay=%s", got)
	}
	if got := imageResumeRetryDelay(20); got != 5*time.Second {
		t.Fatalf("retry delay cap=%s", got)
	}
}

func TestImagePollRetryPolicyIncludesBrokenConnections(t *testing.T) {
	for _, err := range []error{
		errors.New("write tcp 127.0.0.1:1234: write: broken pipe"),
		errors.New("unexpected EOF"),
	} {
		if !isRetryableImagePollError(err) {
			t.Fatalf("error must be retryable: %v", err)
		}
	}
}

func TestAdaptiveImagePollDelayBacksOffToTenSeconds(t *testing.T) {
	base := 3 * time.Second
	want := []time.Duration{3 * time.Second, 4500 * time.Millisecond, 6750 * time.Millisecond, 10 * time.Second, 10 * time.Second}
	for attempt, expected := range want {
		if got := adaptiveImagePollDelay(base, attempt); got != expected {
			t.Fatalf("attempt=%d delay=%s want=%s", attempt, got, expected)
		}
	}
	if got := adaptiveImagePollDelay(12*time.Second, 4); got != 12*time.Second {
		t.Fatalf("explicit slower interval changed to %s", got)
	}
}

func TestResolveConversationImageURLsUsesInitialReferenceBeforePolling(t *testing.T) {
	var conversationHits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/files/download/file_00000000aaaaaaaaaaaaaaaaaaaaaaaa":
			if got := r.URL.Query().Get("conversation_id"); got != "conv-1" {
				t.Errorf("conversation_id query=%q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"download_url": srv.URL + "/image.png"})
		case "/image.png":
			_, _ = w.Write([]byte("PNG"))
		case "/backend-api/conversation/conv-1":
			conversationHits.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	urls, err := client.resolveConversationImageURLs(context.Background(), accounts.Account{AccessToken: "token"}, "conv-1", []string{"file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}, nil, true, nil)
	if err != nil || len(urls) != 1 || urls[0] != srv.URL+"/image.png" || conversationHits.Load() != 0 {
		t.Fatalf("urls=%#v err=%v polls=%d", urls, err, conversationHits.Load())
	}
}

func TestResolveImageURLsFallsBackToLegacyDownloadPath(t *testing.T) {
	var officialHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/files/download/file_00000000aaaaaaaaaaaaaaaaaaaaaaaa":
			officialHits.Add(1)
			http.NotFound(w, r)
		case "/backend-api/files/file_00000000aaaaaaaaaaaaaaaaaaaaaaaa/download":
			_ = json.NewEncoder(w).Encode(map[string]any{"download_url": "https://example.com/image.png"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	urls, err := client.resolveImageURLs(context.Background(), accounts.Account{AccessToken: "token"}, "conv-1", []string{"file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}, nil)
	if err != nil || len(urls) != 1 || urls[0] != "https://example.com/image.png" || officialHits.Load() != 1 {
		t.Fatalf("urls=%#v err=%v official_hits=%d", urls, err, officialHits.Load())
	}
}

func TestResolveImageURLsReturnsReferenceDownloadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired reference", http.StatusForbidden)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	urls, err := client.resolveImageURLs(context.Background(), accounts.Account{AccessToken: "token"}, "conv-1", []string{"file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}, nil)
	if len(urls) != 0 || err == nil || !strings.Contains(err.Error(), "resolve file") || !strings.Contains(err.Error(), "status=403") {
		t.Fatalf("urls=%#v err=%v", urls, err)
	}
}

func TestUploadImageUsesCurrentOfficialFields(t *testing.T) {
	var createBody map[string]any
	var blobHits atomic.Int32
	var confirmHits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files":
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "file-test", "upload_url": srv.URL + "/blob"})
		case r.Method == http.MethodPut && r.URL.Path == "/blob":
			blobHits.Add(1)
			data, _ := io.ReadAll(r.Body)
			if string(data) != "PNGDATA" || r.Header.Get("Content-Type") != "image/png" || r.Header.Get("x-ms-blob-type") != "BlockBlob" {
				t.Errorf("blob request headers=%v body=%q", r.Header, data)
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files/file-test/uploaded":
			confirmHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	input := ImageInput{Data: []byte("PNGDATA"), FileName: "reference.png", MIMEType: "image/png", Width: 32, Height: 24}
	meta, err := client.uploadImage(context.Background(), accounts.Account{Email: "a@example.com", AccessToken: "token"}, input, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if meta.FileID != "file-test" || blobHits.Load() != 1 || confirmHits.Load() != 1 {
		t.Fatalf("meta=%#v blob_hits=%d confirm_hits=%d", meta, blobHits.Load(), confirmHits.Load())
	}
	if createBody["file_name"] != "reference.png" || createBody["file_size"] != float64(len(input.Data)) || createBody["use_case"] != "multimodal" || createBody["mime_type"] != "image/png" {
		t.Fatalf("create body=%#v", createBody)
	}
	if createBody["supports_direct_azure_multipart"] != true || createBody["reset_rate_limits"] != false || createBody["timezone_offset_min"] != float64(-480) {
		t.Fatalf("official upload fields=%#v", createBody)
	}
	if _, found := createBody["width"]; found {
		t.Fatalf("legacy width leaked into create request: %#v", createBody)
	}
	if _, found := createBody["height"]; found {
		t.Fatalf("legacy height leaked into create request: %#v", createBody)
	}
}

func TestUploadImageRetriesTransientCreateFailureOnSameAccount(t *testing.T) {
	var createHits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files":
			if createHits.Add(1) < 3 {
				http.Error(w, "server busy", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "file-retry", "upload_url": srv.URL + "/blob"})
		case r.Method == http.MethodPut && r.URL.Path == "/blob":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files/file-retry/uploaded":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithSleep(func(context.Context, time.Duration) error { return nil }))
	_, err := client.uploadImage(context.Background(), accounts.Account{Email: "a@example.com", AccessToken: "token"}, ImageInput{Data: []byte("PNGDATA"), FileName: "reference.png", MIMEType: "image/png", Width: 32, Height: 24}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if createHits.Load() != maxImageUploadStageAttempts {
		t.Fatalf("create attempts=%d", createHits.Load())
	}
}

func TestGenerateImageGivesUploadAnIndependentBudget(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files":
			time.Sleep(45 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "file-input", "upload_url": srv.URL + "/blob"})
		case r.Method == http.MethodPut && r.URL.Path == "/blob":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files/file-input/uploaded":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = w.Write([]byte(`<html data-build="build" data-seq="1234567"></html>`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prep", "proofofwork": map[string]any{"required": false}})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "sentinel"})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit"})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-upload\",\"message\":{\"author\":{\"role\":\"tool\"},\"content\":{\"parts\":[{\"asset_pointer\":\"file-service://file-output\"}]}}}\n\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/files/download/file-output":
			_ = json.NewEncoder(w).Encode(map[string]any{"download_url": srv.URL + "/result.png"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	client.imageUploadTimeout = 100 * time.Millisecond
	client.imagePreparationTimeout = 30 * time.Millisecond
	client.pollTimeout = 200 * time.Millisecond
	result, err := client.GenerateImage(context.Background(), accounts.Account{Email: "a@example.com", AccessToken: "token"}, ImageRequest{Prompt: "edit", References: []ImageInput{{Data: []byte("PNGDATA"), FileName: "reference.png", MIMEType: "image/png", Width: 32, Height: 24}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConversationID != "conv-upload" || len(result.URLs) != 1 || result.URLs[0] != srv.URL+"/result.png" {
		t.Fatalf("result=%#v", result)
	}
}

func TestConcurrentImageUploadsRecoverFromTransientCreateFailures(t *testing.T) {
	const concurrency = 20
	var mu sync.Mutex
	createHits := map[string]int{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/files":
			mu.Lock()
			createHits[token]++
			hits := createHits[token]
			mu.Unlock()
			if hits == 1 {
				http.Error(w, "server busy", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "file-" + token, "upload_url": srv.URL + "/blob"})
		case r.Method == http.MethodPut && r.URL.Path == "/blob":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/backend-api/files/file-") && strings.HasSuffix(r.URL.Path, "/uploaded"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithSleep(func(context.Context, time.Duration) error { return nil }))
	errorsByWorker := make(chan error, concurrency)
	var workers sync.WaitGroup
	for index := 0; index < concurrency; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			token := fmt.Sprintf("token-%d", index)
			_, err := client.uploadImage(context.Background(), accounts.Account{Email: token + "@example.com", AccessToken: token}, ImageInput{Data: []byte("PNGDATA"), FileName: "reference.png", MIMEType: "image/png", Width: 32, Height: 24}, 1, 1)
			errorsByWorker <- err
		}(index)
	}
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(createHits) != concurrency {
		t.Fatalf("accounts=%d", len(createHits))
	}
	for token, hits := range createHits {
		if hits != 2 {
			t.Fatalf("token=%s create_hits=%d", token, hits)
		}
	}
}
