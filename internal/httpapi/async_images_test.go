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
	"time"
)

func TestStandardAsyncImageGenerationAndStatus(t *testing.T) {
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
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var submitted map[string]any
	if err := json.NewDecoder(response.Body).Decode(&submitted); err != nil {
		t.Fatal(err)
	}
	id, _ := submitted["id"].(string)
	if id == "" || submitted["object"] != "image.task" {
		t.Fatalf("submitted=%#v", submitted)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusRequest, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/images/"+id, nil)
		statusRequest.Header.Set("Authorization", "Bearer k")
		statusResponse, err := http.DefaultClient.Do(statusRequest)
		if err != nil {
			t.Fatal(err)
		}
		var status map[string]any
		_ = json.NewDecoder(statusResponse.Body).Decode(&status)
		statusResponse.Body.Close()
		if status["status"] == "completed" {
			if status["result"] == nil {
				t.Fatalf("completed task has no result: %#v", status)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("async image task did not complete")
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
	_ = writer.WriteField("hd_repair", "true")
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	req, _, err := server.parseEditRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if !req.Async || !req.HDRepair || req.CallbackURL != "https://example.com/hook" {
		t.Fatalf("request=%#v", req)
	}
}
