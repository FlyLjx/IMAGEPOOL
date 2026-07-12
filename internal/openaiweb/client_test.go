package openaiweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
	var prepareSeen atomic.Bool
	var startSeen atomic.Bool
	const expectedTurnstileToken = "dHVybnN0aWxlLXByb29m"
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
			prepareSeen.Store(true)
			if r.Header.Get("OpenAI-Sentinel-Chat-Requirements-Token") != "sentinel" {
				t.Errorf("missing sentinel header")
			}
			if r.Header.Get("OpenAI-Sentinel-Turnstile-Token") != expectedTurnstileToken {
				t.Errorf("missing turnstile header")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "gpt-5-5" || body["client_prepare_state"] != "success" {
				t.Errorf("bad prepare payload: %#v", body)
			}
			hints, _ := body["system_hints"].([]any)
			if len(hints) != 1 || hints[0] != "picture_v2" {
				t.Errorf("bad hints: %#v", hints)
			}
			json.NewEncoder(w).Encode(map[string]any{"conduit_token": "conduit"})
		case r.Method == http.MethodPost && r.URL.Path == "/backend-api/f/conversation":
			startSeen.Store(true)
			if r.Header.Get("X-Conduit-Token") != "conduit" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				t.Errorf("bad start headers: %v", r.Header)
			}
			if r.Header.Get("OpenAI-Sentinel-Turnstile-Token") != expectedTurnstileToken {
				t.Errorf("missing turnstile header")
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "gpt-5-5" || body["client_prepare_state"] != "sent" {
				t.Errorf("bad start payload: %#v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: {\"conversation_id\":\"conv-1\"}\n\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/conversation/conv-1":
			json.NewEncoder(w).Encode(map[string]any{"mapping": map[string]any{"m": map[string]any{"message": map[string]any{"author": map[string]any{"role": "tool"}, "content": map[string]any{"parts": []any{map[string]any{"asset_pointer": "file-service://file_00000000aaaaaaaaaaaaaaaaaaaaaaaa"}}}}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/backend-api/files/file_00000000aaaaaaaaaaaaaaaaaaaaaaaa/download":
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
	if !prepareSeen.Load() || !startSeen.Load() {
		t.Fatal("protocol requests were not seen")
	}
	if result.ConversationID != "conv-1" || len(result.URLs) != 1 || result.URLs[0] != srv.URL+"/image.png" {
		t.Fatalf("bad result: %#v", result)
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
