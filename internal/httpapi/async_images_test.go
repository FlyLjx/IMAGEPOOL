package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageEndpointsRejectAsyncAndRemovedTaskRoutes(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	request, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"draw","async":true,"response_format":"url"}`))
	request.Header.Set("Authorization", "Bearer k")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", response.StatusCode)
	}
	taskRequest, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/image-tasks/generations", strings.NewReader(`{"prompt":"draw"}`))
	taskRequest.Header.Set("Authorization", "Bearer k")
	taskResponse, err := http.DefaultClient.Do(taskRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer taskResponse.Body.Close()
	if taskResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("task endpoint status=%d", taskResponse.StatusCode)
	}
}

func TestCallbackURLRejectsLocalAndPlainHTTP(t *testing.T) {
	for _, value := range []string{"http://example.com/hook", "https://127.0.0.1/hook", "https://[::1]/hook"} {
		if err := validateCallbackURL(context.Background(), value); err == nil {
			t.Fatalf("callback URL %q was accepted", value)
		}
	}
}

func TestCallbackIPClassification(t *testing.T) {
	if !isPublicCallbackIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address was rejected")
	}
	for _, value := range []string{"10.0.0.1", "100.64.0.1", "169.254.1.1", "192.0.2.1", "::1", "fc00::1"} {
		if isPublicCallbackIP(net.ParseIP(value)) {
			t.Fatalf("non-public address %s was accepted", value)
		}
	}
}

func TestSchedulerDiagnostics(t *testing.T) {
	srv := httptest.NewServer(testServer(t))
	defer srv.Close()
	request, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/diagnostics/scheduler", nil)
	request.Header.Set("Authorization", "Bearer k")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var diagnostics schedulerDiagnostics
	if err := json.NewDecoder(response.Body).Decode(&diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics.Tasks.QueueCapacity != 4096 || diagnostics.Tasks.WorkerLimit != 128 || diagnostics.GPT.Total != 1 {
		t.Fatalf("diagnostics=%#v", diagnostics)
	}
}

func TestParseMultipartAsyncImageOptions(t *testing.T) {
	server := newImageInputTestServer(t, 1)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("async", "true")
	_ = writer.WriteField("callback_url", "https://example.com/hook")
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	req, _, err := server.parseEditRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if !req.Async || req.CallbackURL != "https://example.com/hook" {
		t.Fatalf("request=%#v", req)
	}
}
