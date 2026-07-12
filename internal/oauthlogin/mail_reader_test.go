package oauthlogin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestYYDSMailReaderPollsNextUnreadMessage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/messages/next" {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Fatalf("api key header=%q", r.Header.Get("X-API-Key"))
		}
		if r.URL.Query().Get("address") != "account@example.test" || r.URL.Query().Get("wait") != "30" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"message":{"verificationCode":"654321"}}}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reader := NewYYDSMailReader(server.URL+"/v1", "test-key", server.Client())
	code, err := reader.ReadVerificationCode(ctx, "account@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if code != "654321" || calls.Load() != 2 {
		t.Fatalf("code=%q calls=%d", code, calls.Load())
	}
}

func TestYYDSMailReaderDoesNotExposeAPIKeyInErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"error":"not authorized","errorCode":"invalid_api_key"}`))
	}))
	defer server.Close()

	_, err := NewYYDSMailReader(server.URL, "private-api-key", server.Client()).ReadVerificationCode(context.Background(), "account@example.test")
	if err == nil || !strings.Contains(err.Error(), "invalid_api_key") || strings.Contains(err.Error(), "private-api-key") {
		t.Fatalf("err=%v", err)
	}
}
