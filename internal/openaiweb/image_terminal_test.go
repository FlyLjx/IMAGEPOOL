package openaiweb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"imagepool/internal/accounts"
	"imagepool/internal/browsertransport"
	"imagepool/internal/config"
)

func TestFindImageGenerationTerminalError(t *testing.T) {
	err := findImageGenerationTerminalError(map[string]any{
		"message": map[string]any{
			"author":   map[string]any{"role": "tool"},
			"metadata": map[string]any{"image_gen_async": true, "status": "server_timeout"},
		},
	})
	if !errors.Is(err, ErrImageGenerationTerminated) {
		t.Fatalf("err=%v", err)
	}
	if err := findImageGenerationTerminalError(map[string]any{"metadata": map[string]any{"status": "in_progress"}}); err != nil {
		t.Fatalf("non-terminal status returned error: %v", err)
	}
	if err := findImageGenerationTerminalError(map[string]any{
		"message": map[string]any{
			"author":   map[string]any{"role": "tool", "name": "python"},
			"metadata": map[string]any{"status": "server_timeout"},
		},
	}); err != nil {
		t.Fatalf("non-image tool status returned error: %v", err)
	}
}

func TestFindImageGenerationTerminalErrorFromCurrentToolPayload(t *testing.T) {
	err := findImageGenerationTerminalError(map[string]any{
		"async_status": float64(4),
		"mapping": map[string]any{
			"tool": map[string]any{
				"message": map[string]any{
					"author": map[string]any{"role": "tool", "name": "t2uay3k.sj1i4kz"},
					"content": map[string]any{"content_type": "text", "parts": []any{
						"We experienced an error when generating images.",
					}},
					"metadata": map[string]any{"is_error": true},
				},
			},
		},
	})
	if !errors.Is(err, ErrImageGenerationTerminated) {
		t.Fatalf("err=%v", err)
	}
}

func TestStartImageGenerationStopsOnTerminalToolStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/f/conversation" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"conversation_id\":\"conv-1\",\"message\":{\"author\":{\"role\":\"tool\"},\"metadata\":{\"image_gen_async\":true,\"status\":\"server_timeout\"}}}\n\n"))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = server.URL
	client := NewClient(cfg, WithHTTPClient(server.Client()))
	_, _, _, err := client.startImageGeneration(context.Background(), accounts.Account{AccessToken: "token"}, "draw", "auto", chatRequirements{}, "conduit", "trace", "parent", nil)
	if !errors.Is(err, ErrImageGenerationTerminated) {
		t.Fatalf("err=%v", err)
	}
}

func TestPollImageResultsStopsOnTerminalToolStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/conversation/conv-1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"mapping":{"tool":{"message":{"author":{"role":"tool"},"metadata":{"image_gen_async":true,"status":"interrupted"}}}}}`))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.ChatGPTBaseURL = server.URL
	client := NewClient(cfg, WithHTTPClient(server.Client()))
	_, _, err := client.pollImageResults(context.Background(), accounts.Account{AccessToken: "token"}, "conv-1", nil, nil)
	if !errors.Is(err, ErrImageGenerationTerminated) {
		t.Fatalf("err=%v", err)
	}
}

func TestNewClientUsesUnboundedStreamClient(t *testing.T) {
	client := NewClient(config.Default())
	streamClient := client.streamClientFor(accounts.Account{})
	if streamClient == nil {
		t.Fatal("stream client is nil")
	}
	if streamClient.Timeout != 0 {
		t.Fatalf("stream client timeout=%v", streamClient.Timeout)
	}

	cfg := config.Default()
	cfg.UpstreamTransport = "tls_client"
	tlsClient := NewClient(cfg)
	account := accounts.Account{AccessToken: "token"}
	normalClient := tlsClient.clientFor(account, false)
	tlsStreamClient := tlsClient.streamClientFor(account)
	if tlsStreamClient == nil {
		t.Fatal("TLS stream client is nil")
	}
	if tlsStreamClient.Timeout != 0 {
		t.Fatalf("tls stream client timeout=%v", tlsStreamClient.Timeout)
	}
	if browsertransport.CookieJarForHTTPClient(tlsStreamClient) != browsertransport.CookieJarForHTTPClient(normalClient) {
		t.Fatal("stream client must retain the established TLS cookie jar")
	}
}
