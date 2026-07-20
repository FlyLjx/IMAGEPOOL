package openaiweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestExtractConversationAndImageReferences(t *testing.T) {
	payload := `{"conversation_id":"conv-1","content":{"parts":[{"asset_pointer":"file-service://file_abc"},"sediment://sed_1 file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"]}}`
	if got := ExtractConversationID(payload); got != "conv-1" {
		t.Fatalf("conversation id = %q", got)
	}
	var v any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatal(err)
	}
	files, sediments := ExtractImageReferenceIDs(v)
	if len(files) != 2 || files[0] != "file_abc" || files[1] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("files=%#v", files)
	}
	if len(sediments) != 1 || sediments[0] != "sed_1" {
		t.Fatalf("sediments=%#v", sediments)
	}
}

func TestImagePromptForWebKeepsOriginalEditPrompt(t *testing.T) {
	got := imagePromptForWeb("把背景改成海边", true, "1536x864", "high")
	if got != "把背景改成海边" {
		t.Fatalf("prompt=%q", got)
	}
}

func TestImagePromptForWebKeepsPlainPromptForAutoTextImage(t *testing.T) {
	got := imagePromptForWeb("画一只猫", false, "auto", "auto")
	if got != "画一只猫" {
		t.Fatalf("prompt=%q", got)
	}
}

func TestExtractGeneratedImageReferenceIDsSkipsUserAttachments(t *testing.T) {
	payload := `{
		"mapping": {
			"user": {"message": {"author": {"role": "user"}, "content": {"parts": [
				{"asset_pointer": "file-service://file_uploaded"},
				"sediment://sed_uploaded"
			]}}},
			"tool": {"message": {"author": {"role": "tool"}, "content": {"parts": [
				{"asset_pointer": "file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"},
				"sediment://sed_generated"
			]}}}
		}
	}`
	var v any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		t.Fatal(err)
	}
	files, sediments := ExtractGeneratedImageReferenceIDs(v)
	if len(files) != 1 || files[0] != "file_00000000aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("files=%#v", files)
	}
	if len(sediments) != 1 || sediments[0] != "sed_generated" {
		t.Fatalf("sediments=%#v", sediments)
	}
}

func TestGenerateImageReverseProtocol(t *testing.T) {
	var prepareCount atomic.Int32
	var startSeen atomic.Bool
	var turnTraceID string
	const expectedTurnstileToken = "dHVybnN0aWxlLXByb29m"
	expectedPrepareStates := []string{"none"}
	expectedConduitHeaders := []string{"no-token"}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.Write([]byte(`<html data-build="build"><script src="/c/test/_abc.js"></script></html>`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			p, _ := body["p"].(string)
			dx := turnstileDXForTest(t, p, []any{[]any{3, "turnstile-proof"}})
			json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prep", "proofofwork": map[string]any{"required": false}, "turnstile": map[string]any{"required": true, "dx": dx}})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["turnstile_token"] != expectedTurnstileToken {
				t.Errorf("turnstile token=%#v", body["turnstile_token"])
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "sentinel"})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation/prepare":
			prepareIndex := int(prepareCount.Add(1)) - 1
			if prepareIndex >= len(expectedPrepareStates) {
				t.Errorf("too many prepare requests: %d", prepareIndex+1)
				http.Error(w, "too many prepare requests", http.StatusBadRequest)
				return
			}
			if r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token") != "sentinel" {
				t.Errorf("missing sentinel header")
			}
			if r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Prepare-Token") != "prep" {
				t.Errorf("missing sentinel prepare header")
			}
			if r.Header.Get("OpenAI-Sentinel-Turnstile-Token") != expectedTurnstileToken {
				t.Errorf("missing turnstile header")
			}
			if r.Header.Get("OAI-Client-Version") != "prod-de97061a1c9aff3931a7342defd6241031cd316a" || r.Header.Get("OAI-Client-Build-Number") != "8160987" {
				t.Errorf("outdated client identity headers: version=%q build=%q", r.Header.Get("OAI-Client-Version"), r.Header.Get("OAI-Client-Build-Number"))
			}
			if got := r.Header.Get("X-Conduit-Token"); got != expectedConduitHeaders[prepareIndex] {
				t.Errorf("prepare %d conduit header=%q", prepareIndex, got)
			}
			if got := r.Header.Get("X-Oai-Turn-Trace-Id"); got == "" {
				t.Errorf("prepare %d missing turn trace ID", prepareIndex)
			} else if turnTraceID == "" {
				turnTraceID = got
			} else if got != turnTraceID {
				t.Errorf("prepare %d turn trace ID=%q want=%q", prepareIndex, got, turnTraceID)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "auto" || body["client_prepare_state"] != expectedPrepareStates[prepareIndex] || body["thinking_effort"] != "standard" {
				t.Errorf("bad prepare payload: %#v", body)
			}
			hints, _ := body["system_hints"].([]any)
			if len(hints) != 0 {
				t.Errorf("bad hints: %#v", hints)
			}
			partial, _ := body["partial_query"].(map[string]any)
			if partial["author"] == nil || partial["content"] == nil {
				t.Errorf("prepare %d missing partial_query: %#v", prepareIndex, body)
			}
			json.NewEncoder(w).Encode(map[string]any{"conduit_token": fmt.Sprintf("conduit-%d", prepareIndex+1)})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation":
			startSeen.Store(true)
			if r.Header.Get("X-Conduit-Token") != "conduit-1" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				t.Errorf("bad start headers: %v", r.Header)
			}
			if r.Header.Get("X-Oai-Turn-Trace-Id") != turnTraceID || turnTraceID == "" {
				t.Errorf("start turn trace ID=%q want=%q", r.Header.Get("X-Oai-Turn-Trace-Id"), turnTraceID)
			}
			if r.Header.Get("OAI-Echo-Logs") != "0,943,1,65876,0,68124,1,68930" || r.Header.Get("OAI-Telemetry") != "[1,null]" {
				t.Errorf("missing start telemetry headers: %v", r.Header)
			}
			if r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Prepare-Token") != "prep" {
				t.Errorf("missing start sentinel prepare header")
			}
			if r.Header.Get("OpenAI-Sentinel-Turnstile-Token") != expectedTurnstileToken {
				t.Errorf("missing turnstile header")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "auto" || body["client_prepare_state"] != "sent" || body["thinking_effort"] != "standard" {
				t.Errorf("bad start payload: %#v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"conversation_id\":\"conv-1\"}\n\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/conversation/conv-1":
			json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{"m": map[string]any{"message": map[string]any{"author": map[string]any{"role": "tool"}, "content": map[string]any{"parts": []any{map[string]any{"asset_pointer": "file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}}}}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/files/download/file_00000000aaaaaaaaaaaaaaaaaaaaaaaa":
			if got := r.URL.Query().Get("conversation_id"); got != "conv-1" {
				t.Errorf("download conversation_id=%q", got)
			}
			json.NewEncoder(w).Encode(map[string]any{"download_url": srv.URL + "/image.png"})
		case r.Method == http.MethodGet && r.URL.Path == "/image.png":
			w.Write([]byte("PNG"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	cfg.ImagePollTimeoutSecs = 0.2
	cfg.ImagePollIntervalSecs = 0.01
	cfg.ImageSettleEnabled = false
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithIDGenerator(func() string { return "00000000-0000-4000-8000-000000000001" }))
	result, err := client.GenerateImage(context.Background(), accounts.Account{Email: "a@example.com", AccessToken: "tok"}, ImageRequest{Prompt: "draw", Model: "gpt-image-2"})
	if err != nil {
		t.Fatal(err)
	}
	if prepareCount.Load() != 1 || !startSeen.Load() {
		t.Fatal("protocol requests were not seen")
	}
	if result.ConversationID != "conv-1" || len(result.URLs) != 1 || result.URLs[0] != srv.URL+"/image.png" {
		t.Fatalf("bad result: %#v", result)
	}
}

func TestGenerateImageUsesOneBudgetForStreamAndPolling(t *testing.T) {
	pollStarted := make(chan struct{}, 1)
	pollDone := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte("<html></html>"))
		case "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prep", "proofofwork": map[string]any{"required": false}})
		case "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "sentinel"})
		case "/backend-api/f/conversation/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit"})
		case "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"resume_conversation_token\",\"token\":\"resume-token\",\"conversation_id\":\"conv-1\"}\n\ndata: [DONE]\n\n"))
		case "/backend-api/conversation/conv-1":
			select {
			case pollStarted <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			select {
			case pollDone <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	client.pollTimeout = 40 * time.Millisecond
	started := time.Now()
	_, err := client.GenerateImage(context.Background(), accounts.Account{AccessToken: "token"}, ImageRequest{Prompt: "draw"})
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("generation exceeded its shared budget for %s", elapsed)
	}
	select {
	case <-pollStarted:
	case <-time.After(time.Second):
		t.Fatal("conversation poll did not start")
	}
	select {
	case <-pollDone:
	case <-time.After(time.Second):
		t.Fatal("conversation poll context was not canceled")
	}
}

func TestGenerateImageBoundsPreparationBeforeSubmittingConversation(t *testing.T) {
	bootstrapCanceled := make(chan struct{}, 1)
	var starts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			select {
			case <-r.Context().Done():
				select {
				case bootstrapCanceled <- struct{}{}:
				default:
				}
			case <-time.After(time.Second):
			}
		case "/backend-api/f/conversation":
			starts.Add(1)
			http.Error(w, "should not submit after preparation timeout", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	client.imagePreparationTimeout = 35 * time.Millisecond
	client.pollTimeout = 80 * time.Millisecond
	started := time.Now()
	_, err := client.GenerateImage(context.Background(), accounts.Account{AccessToken: "token"}, ImageRequest{Prompt: "draw"})
	if !errors.Is(err, ErrImagePreparationTimeout) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("preparation exceeded its bounded budget: %s", elapsed)
	}
	if starts.Load() != 0 {
		t.Fatalf("submitted %d conversations after preparation timeout", starts.Load())
	}
	select {
	case <-bootstrapCanceled:
	case <-time.After(time.Second):
		t.Fatal("bootstrap request was not canceled")
	}
}

func TestImagePreparationErrorPreservesTurnstileVMCapacity(t *testing.T) {
	client := NewClient(config.Default())
	capacity := fmt.Errorf("%w: %w", ErrTurnstileVMCapacity, context.DeadlineExceeded)
	err := client.imagePreparationError(context.Background(), capacity)
	if !errors.Is(err, ErrTurnstileVMCapacity) {
		t.Fatalf("capacity sentinel was lost: %v", err)
	}
	if errors.Is(err, ErrImagePreparationTimeout) {
		t.Fatalf("capacity congestion was relabeled as account preparation timeout: %v", err)
	}
}

func TestGenerateImagePreservesFullGenerationBudgetAfterPreparation(t *testing.T) {
	generationStarted := make(chan time.Time, 1)
	generationCanceled := make(chan time.Time, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte("<html></html>"))
		case "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prep", "proofofwork": map[string]any{"required": false}})
		case "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "sentinel"})
		case "/backend-api/f/conversation/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit"})
		case "/backend-api/f/conversation":
			w.Header().Set("Content-Type", "text/event-stream")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case generationStarted <- time.Now():
			default:
			}
			<-r.Context().Done()
			select {
			case generationCanceled <- time.Now():
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	client.imagePreparationTimeout = 45 * time.Millisecond
	client.pollTimeout = 75 * time.Millisecond
	client.imageStreamIdleTimeout = time.Second
	_, err := client.GenerateImage(context.Background(), accounts.Account{AccessToken: "token"}, ImageRequest{Prompt: "draw"})
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err=%v", err)
	}
	var began time.Time
	select {
	case began = <-generationStarted:
	case <-time.After(time.Second):
		t.Fatal("generation request did not start")
	}
	select {
	case canceled := <-generationCanceled:
		if elapsed := canceled.Sub(began); elapsed < 55*time.Millisecond || elapsed > 180*time.Millisecond {
			t.Fatalf("generation window=%s, want approximately 75ms", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("generation stream was not canceled")
	}
}

func TestPrepareImageConversationIncludesReferenceMetadata(t *testing.T) {
	var prepareCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/f/conversation/prepare" {
			http.NotFound(w, r)
			return
		}
		prepareCount++
		if got := r.Header.Get("X-Conduit-Token"); got != "no-token" {
			t.Errorf("prepare %d conduit header=%q want=%q", prepareCount, got, "no-token")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mimeTypes, _ := body["attachment_mime_types"].([]any)
		if len(mimeTypes) != 1 || mimeTypes[0] != "image/png" {
			t.Errorf("prepare %d attachment_mime_types=%#v", prepareCount, mimeTypes)
		}
		if body["client_prepare_state"] != "none" {
			t.Errorf("prepare state=%#v", body["client_prepare_state"])
		}
		partial, _ := body["partial_query"].(map[string]any)
		content, _ := partial["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		if content["content_type"] != "multimodal_text" || len(parts) != 2 {
			t.Errorf("prepare partial_query content=%#v", content)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit-1"})
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithIDGenerator(func() string { return "00000000-0000-4000-8000-000000000001" }))
	refs := []uploadMeta{{FileID: "file-reference", FileName: "reference.png", FileSize: 128, MIMEType: "image/png", Width: 16, Height: 16}}
	conduit, traceID, parentMessageID, err := client.prepareImageConversation(context.Background(), accounts.Account{AccessToken: "token"}, "edit it", "auto", chatRequirements{}, refs)
	if err != nil {
		t.Fatal(err)
	}
	if prepareCount != 1 || conduit != "conduit-1" || traceID == "" || parentMessageID == "" {
		t.Fatalf("prepare count=%d conduit=%q trace=%q parent=%q", prepareCount, conduit, traceID, parentMessageID)
	}
}

func TestGenerateImagePrepareFallbackUsesSuccessState(t *testing.T) {
	var prepareCount atomic.Int32
	var startState string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Write([]byte(`<html data-build="build"><script src="/c/test/_abc.js"></script></html>`))
		case "/backend-api/sentinel/chat-requirements/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prep", "proofofwork": map[string]any{"required": false}})
		case "/backend-api/sentinel/chat-requirements/finalize":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "sentinel"})
		case "/backend-api/f/conversation/prepare":
			index := prepareCount.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["partial_query"]; !ok {
				t.Errorf("prepare %d missing partial_query: %#v", index, body)
			}
			if index == 1 {
				if body["client_prepare_state"] != "none" {
					t.Errorf("first prepare state=%#v", body["client_prepare_state"])
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"detail": "no conduit for old prepare state"})
				return
			}
			if body["client_prepare_state"] != "success" {
				t.Errorf("fallback prepare state=%#v", body["client_prepare_state"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"conduit_token": "fallback-conduit"})
		case "/backend-api/f/conversation":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			startState = fmt.Sprint(body["client_prepare_state"])
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"conversation_id\":\"conv-fallback\"}\n\n"))
		case "/backend-api/conversation/conv-fallback":
			_ = json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{"m": map[string]any{"message": map[string]any{"author": map[string]any{"role": "tool"}, "content": map[string]any{"parts": []any{map[string]any{"asset_pointer": "file-service://file_00000000bbbbbbbbbbbbbbbbbbbbbbbb"}}}}}}})
		case "/backend-api/files/download/file_00000000bbbbbbbbbbbbbbbbbbbbbbbb":
			_ = json.NewEncoder(w).Encode(map[string]any{"download_url": srv.URL + "/fallback.png"})
		case "/fallback.png":
			w.Write([]byte("PNG"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	result, err := client.GenerateImage(context.Background(), accounts.Account{Email: "a@example.com", AccessToken: "tok"}, ImageRequest{Prompt: "draw", Model: "gpt-image-2"})
	if err != nil {
		t.Fatal(err)
	}
	if prepareCount.Load() != 2 || startState != "success" || len(result.URLs) != 1 {
		t.Fatalf("prepare=%d startState=%q result=%#v", prepareCount.Load(), startState, result)
	}
}

func TestListModelsUsesOfficialModelsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Write([]byte("<html></html>"))
		case "/backend-api/models":
			if r.URL.Query().Get("history_and_training_disabled") != "false" {
				t.Errorf("bad query: %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"slug": "gpt-5-5"}, map[string]any{"slug": "gpt-5-3"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	models, err := NewClient(cfg, WithHTTPClient(srv.Client())).ListModels(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "gpt-5-5" || models[1] != "gpt-5-3" {
		t.Fatalf("models=%#v", models)
	}
}

func TestCheckImageReadyCompletesSentinelWithoutStartingConversation(t *testing.T) {
	var prepared atomic.Bool
	var finalized atomic.Bool
	var conversationStarted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = w.Write([]byte(`<html data-build="build"><script src="/c/test/_abc.js"></script></html>`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
			prepared.Store(true)
			_, _ = w.Write([]byte(`{"prepare_token":"prep","proofofwork":{"required":false}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
			finalized.Store(true)
			_, _ = w.Write([]byte(`{"token":"sentinel"}`))
		case r.URL.Path == "/backend-api/f/conversation/prepare" || r.URL.Path == "/backend-api/f/conversation":
			conversationStarted.Store(true)
			http.Error(w, "unexpected image request", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	if err := client.CheckImageReady(context.Background(), "token"); err != nil {
		t.Fatal(err)
	}
	if !prepared.Load() || !finalized.Load() || conversationStarted.Load() {
		t.Fatalf("prepare=%v finalize=%v conversation=%v", prepared.Load(), finalized.Load(), conversationStarted.Load())
	}
}

func TestDownloadImageForSameUpstreamUsesAccountHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte("image-data"))
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	data, err := client.DownloadImageFor(context.Background(), accounts.Account{AccessToken: "token"}, srv.URL+"/image.png")
	if err != nil || string(data) != "image-data" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestDownloadImageForSameUpstreamPreservesAuthenticationFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		http.Error(w, `{"error":{"code":"token_revoked"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	_, err := NewClient(cfg, WithHTTPClient(srv.Client())).DownloadImageFor(context.Background(), accounts.Account{AccessToken: "token"}, srv.URL+"/image.png")
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusUnauthorized || !IsAuthenticationError(err) {
		t.Fatalf("err=%#v", err)
	}
}

func TestGetAccountInfoUsesOfficialMeEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte("<html></html>"))
		case "/backend-api/me":
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Errorf("authorization=%q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"email":"person@example.test"}`))
		case "/backend-api/conversation/init":
			if r.Method != http.MethodPost {
				t.Errorf("method=%s", r.Method)
			}
			_, _ = w.Write([]byte(`{"default_model_slug":"gpt-image-2","limits_progress":[{"feature_name":"image_gen","remaining":7,"reset_after":"2026-07-12T00:00:00Z"}]}`))
		case "/backend-api/accounts/check/v4-2023-04-27":
			_, _ = w.Write([]byte(`{"accounts":{"default":{"account":{"plan_type":"plus"}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	info, err := NewClient(cfg, WithHTTPClient(srv.Client())).GetAccountInfo(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if info.Email != "person@example.test" || info.Type != "plus" || info.Quota != 7 || info.ImageQuotaUnknown || info.DefaultModelSlug != "gpt-image-2" {
		t.Fatalf("info=%#v", info)
	}
}

func TestDebugRequestUsesWebHeadersAndReturnsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte("<html></html>"))
		case "/backend-api/me":
			if r.Header.Get("X-OpenAI-Target-Path") != "/backend-api/me" || r.Header.Get("Authorization") != "Bearer token" {
				t.Errorf("headers=%#v", r.Header)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"email":"person@example.test"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	result, err := NewClient(cfg, WithHTTPClient(srv.Client())).Debug(context.Background(), DebugRequest{Method: "GET", Path: "/backend-api/me", AccessToken: "token", Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	body, ok := result.Body.(map[string]any)
	if !ok || result.Status != http.StatusOK || body["email"] != "person@example.test" || result.RequestHeaders["Authorization"] != "Bearer ***" {
		t.Fatalf("result=%#v", result)
	}
	if _, err := NewClient(cfg).Debug(context.Background(), DebugRequest{Method: "GET", Path: "https://outside.example/backend-api/me"}); err == nil {
		t.Fatal("expected foreign URL rejection")
	}
}

func TestGenerateTextReverseProtocol(t *testing.T) {
	var conversationSeen atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.Write([]byte(`<html><script src="/c/test/_abc.js"></script></html>`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
			json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prep", "proofofwork": map[string]any{"required": false}})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
			json.NewEncoder(w).Encode(map[string]any{"token": "sentinel"})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/conversation":
			conversationSeen.Store(true)
			if r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token") != "sentinel" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				t.Errorf("bad headers: %v", r.Header)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "auto" || body["force_use_sse"] != true {
				t.Errorf("bad text payload: %#v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"conversation_id\":\"conv-text\",\"message\":{\"author\":{\"role\":\"assistant\"},\"content\":{\"parts\":[\"Hel\"]}}}\n\n"))
			w.Write([]byte("data: {\"p\":\"/message/content/parts/0\",\"o\":\"append\",\"v\":\"lo\"}\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithIDGenerator(func() string { return "00000000-0000-4000-8000-000000000001" }))
	result, err := client.GenerateText(context.Background(), accounts.Account{Email: "a@example.com", AccessToken: "tok"}, ChatRequest{Model: "auto", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !conversationSeen.Load() || result.Text != "Hello" || result.ConversationID != "conv-text" {
		t.Fatalf("seen=%v result=%#v", conversationSeen.Load(), result)
	}
}

func TestSearchReverseProtocol(t *testing.T) {
	var prepareSeen atomic.Bool
	var startSeen atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation/prepare":
			prepareSeen.Store(true)
			if r.Header.Get("X-Conduit-Token") != "no-token" {
				t.Errorf("missing no-token conduit header")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "gpt-5-5" {
				t.Errorf("bad search prepare payload: %#v", body)
			}
			json.NewEncoder(w).Encode(map[string]any{"conduit_token": "search-conduit"})
		case r.Method == http.MethodGet && r.URL.Path == "/":
			w.Write([]byte(`<html><script src="/c/test/_abc.js"></script></html>`))
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/prepare":
			json.NewEncoder(w).Encode(map[string]any{"prepare_token": "prep", "proofofwork": map[string]any{"required": false}})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/sentinel/chat-requirements/finalize":
			json.NewEncoder(w).Encode(map[string]any{"token": "sentinel"})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation":
			startSeen.Store(true)
			if r.Header.Get("X-Conduit-Token") != "search-conduit" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				t.Errorf("bad search start headers: %v", r.Header)
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["force_use_search"] != true || body["client_reported_search_source"] != "conversation_composer_web_icon" {
				t.Errorf("bad search start payload: %#v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"conversation_id\":\"conv-search\"}\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/conversation/conv-search":
			json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{"a": map[string]any{"message": map[string]any{
				"id":          "msg-1",
				"create_time": 1,
				"author":      map[string]any{"role": "assistant"},
				"metadata":    map[string]any{"finish_details": map[string]any{"type": "finished_successfully"}, "citations": []any{map[string]any{"title": "Example", "url": "https://example.com", "snippet": "snippet"}}},
				"content":     map[string]any{"parts": []any{"answer https://fallback.example"}},
			}}}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()), WithIDGenerator(func() string { return "00000000-0000-4000-8000-000000000001" }))
	result, err := client.Search(context.Background(), accounts.Account{Email: "a@example.com", AccessToken: "tok"}, SearchRequest{Prompt: "query", Model: "gpt-5-5", TimeoutSecs: 1, PollIntervalSecs: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	if !prepareSeen.Load() || !startSeen.Load() || result.ConversationID != "conv-search" || result.Answer == "" || len(result.Sources) != 2 {
		t.Fatalf("prepare=%v start=%v result=%#v", prepareSeen.Load(), startSeen.Load(), result)
	}
}

func TestAccountProxyOverridesRuntimeClient(t *testing.T) {
	var hits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.String() != "http://upstream.example.test/check" {
			t.Errorf("proxy request url=%q", r.URL.String())
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()
	cfg := config.Default()
	cfg.ProxyRuntime.Enabled = false
	client := NewClient(cfg)
	req, err := http.NewRequest(http.MethodGet, "http://upstream.example.test/check", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.clientFor(accounts.Account{Proxy: proxyServer.URL}, false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || hits.Load() != 1 {
		t.Fatalf("status=%d proxyHits=%d", resp.StatusCode, hits.Load())
	}
}

func TestBootstrapResourcesCacheReusesAndExpiresPerAccount(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		build := hits.Add(1)
		_, _ = fmt.Fprintf(w, `<html data-build="build-%d"><script src="/assets/%d.js"></script></html>`, build, build)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	clock := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return clock }
	account := accounts.Account{AccessToken: "account-a"}

	firstScripts, firstBuild, err := client.bootstrapWithResources(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	firstScripts[0] = "mutated-by-caller"
	secondScripts, secondBuild, err := client.bootstrapWithResources(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 || firstBuild != "build-1" || secondBuild != "build-1" || len(secondScripts) != 1 || secondScripts[0] != "/assets/1.js" {
		t.Fatalf("hits=%d first=%#v/%q second=%#v/%q", hits.Load(), firstScripts, firstBuild, secondScripts, secondBuild)
	}

	if _, _, err := client.bootstrapWithResources(context.Background(), accounts.Account{AccessToken: "account-b"}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("different account reused cache: hits=%d", hits.Load())
	}
	if bootstrapResourcesKey(accounts.Account{AccessToken: "account-a", Proxy: "http://proxy-one"}) == bootstrapResourcesKey(accounts.Account{AccessToken: "account-a", Proxy: "http://proxy-two"}) {
		t.Fatal("bootstrap cache key does not distinguish account proxies")
	}

	clock = clock.Add(bootstrapResourcesTTL)
	if _, build, err := client.bootstrapWithResources(context.Background(), account); err != nil || build != "build-3" {
		t.Fatalf("expired cache result build=%q err=%v", build, err)
	}
	if hits.Load() != 3 {
		t.Fatalf("expired cache did not refresh: hits=%d", hits.Load())
	}
}

func TestBootstrapResourcesCacheDoesNotCacheFailures(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "temporary bootstrap failure", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`<html data-build="build"><script src="/assets/app.js"></script></html>`))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	account := accounts.Account{AccessToken: "account"}
	if _, _, err := client.bootstrapWithResources(context.Background(), account); err == nil {
		t.Fatal("expected initial bootstrap failure")
	}
	if _, _, err := client.bootstrapWithResources(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("failed bootstrap was cached: hits=%d", hits.Load())
	}
}

func TestBootstrapResourcesCacheCoalescesConcurrentMisses(t *testing.T) {
	var hits atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		enteredOnce.Do(func() { close(entered) })
		<-release
		_, _ = w.Write([]byte(`<html data-build="build"><script src="/assets/app.js"></script></html>`))
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = srv.URL
	client := NewClient(cfg, WithHTTPClient(srv.Client()))
	const workers = 12
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := client.bootstrapWithResources(context.Background(), accounts.Account{AccessToken: "account"})
			errs <- err
		}()
	}
	close(start)
	<-entered
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("concurrent bootstrap requests=%d want 1", hits.Load())
	}
}
