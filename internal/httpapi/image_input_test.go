package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepool/internal/config"
	"imagepool/internal/openaiweb"
)

func newImageInputTestServer(t *testing.T, requestTimeoutSecs float64) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.RequestTimeoutSecs = requestTimeoutSecs
	cfg.AuthKeyFile = filepath.Join(t.TempDir(), "auth-keys.json")
	cfg.ImageOutputDir = filepath.Join(t.TempDir(), "images")
	cfg.CallLogFile = filepath.Join(t.TempDir(), "calls.json")
	cfg.ImageTagsFile = filepath.Join(t.TempDir(), "tags.json")
	cfg.RegisterFile = filepath.Join(t.TempDir(), "register.json")
	server, ok := newTestServer(cfg).(*Server)
	if !ok {
		t.Fatal("test server is not an HTTP API server")
	}
	return server
}

func imageInputTestPNG(t *testing.T, pixel color.Color) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, 1, 1))
	imageData.Set(0, 0, pixel)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageData); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestParseEditRequestDownloadsReferencesConcurrentlyAndPreservesOrder(t *testing.T) {
	imageData := imageInputTestPNG(t, color.RGBA{R: 0xff, A: 0xff})
	started := make(chan string, 2)
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- r.URL.Path:
		case <-r.Context().Done():
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData)
	}))
	defer remote.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	server := newImageInputTestServer(t, 1)
	body := fmt.Sprintf(`{"images":[%q,%q]}`, remote.URL+"/first.png", remote.URL+"/second.png")
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	type parsed struct {
		references []openaiweb.ImageInput
		err        error
	}
	parsedRequest := make(chan parsed, 1)
	go func() {
		req, _, err := server.parseEditRequest(request)
		parsedRequest <- parsed{references: req.References, err: err}
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("reference downloads were not started concurrently")
		}
	}
	close(release)

	result := <-parsedRequest
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.references) != 2 {
		t.Fatalf("reference count=%d", len(result.references))
	}
	if result.references[0].FileName != "first.png" || result.references[1].FileName != "second.png" {
		t.Fatalf("reference order=%q, %q", result.references[0].FileName, result.references[1].FileName)
	}
}

func TestParseEditRequestSupportsMultipartImageArrayFields(t *testing.T) {
	server := newImageInputTestServer(t, 1)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image[]", "ref.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(imageInputTestPNG(t, color.RGBA{G: 0xff, A: 0xff})); err != nil {
		t.Fatal(err)
	}
	part, err = writer.CreateFormFile("image[1]", "ref-2.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(imageInputTestPNG(t, color.RGBA{B: 0xff, A: 0xff})); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("prompt", "edit prompt"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	req, _, err := server.parseEditRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if req.Prompt != "edit prompt" {
		t.Fatalf("prompt=%q", req.Prompt)
	}
	if len(req.References) != 2 {
		t.Fatalf("reference count=%d, want 2", len(req.References))
	}
	if req.References[0].FileName != "ref.png" || req.References[1].FileName != "ref-2.png" {
		t.Fatalf("reference order=%q, %q", req.References[0].FileName, req.References[1].FileName)
	}
}

func TestParseEditRequestSupportsJSONImageAliases(t *testing.T) {
	imageData := imageInputTestPNG(t, color.RGBA{R: 0x88, A: 0xff})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData)
	}))
	defer remote.Close()

	server := newImageInputTestServer(t, 1)
	body := fmt.Sprintf(`{"image_urls":[%q],"reference_images":[{"image_url":{"url":%q}}],"input_image":%q}`,
		remote.URL+"/first.png",
		remote.URL+"/second.png",
		remote.URL+"/third.png",
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	req, _, err := server.parseEditRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.References) != 3 {
		t.Fatalf("reference count=%d, want 3", len(req.References))
	}
	wantNames := []string{"first.png", "second.png", "third.png"}
	for index, want := range wantNames {
		if req.References[index].FileName != want {
			t.Fatalf("reference %d filename=%q, want %q", index, req.References[index].FileName, want)
		}
	}
}

func TestParseEditRequestCancelsRemoteDownloadWhenCallerCancels(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer remote.Close()

	server := newImageInputTestServer(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := fmt.Sprintf(`{"image":%q}`, remote.URL+"/slow.png")
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	errCh := make(chan error, 1)
	go func() {
		_, _, err := server.parseEditRequest(request)
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("remote download did not start")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parseEditRequest did not return after request cancellation")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("remote request did not receive cancellation")
	}
}

func TestParseEditRequestCancelsSiblingDownloadAfterFailure(t *testing.T) {
	slowStarted := make(chan struct{}, 1)
	slowCanceled := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow.png":
			slowStarted <- struct{}{}
			<-r.Context().Done()
			slowCanceled <- struct{}{}
		case "/failed.png":
			select {
			case <-slowStarted:
			case <-r.Context().Done():
				return
			}
			http.Error(w, "expired image", http.StatusForbidden)
		}
	}))
	defer remote.Close()

	server := newImageInputTestServer(t, 1)
	body := fmt.Sprintf(`{"images":[%q,%q]}`, remote.URL+"/slow.png", remote.URL+"/failed.png")
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	_, _, err := server.parseEditRequest(request)
	if err == nil || !strings.Contains(err.Error(), "status=403") {
		t.Fatalf("error=%v, want failed reference status", err)
	}
	select {
	case <-slowCanceled:
	case <-time.After(time.Second):
		t.Fatal("slow sibling was not canceled after another reference failed")
	}
}

func TestParseEditRequestLimitsRemoteBodyReadDuration(t *testing.T) {
	started := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer remote.Close()

	server := newImageInputTestServer(t, 0.05)
	body := fmt.Sprintf(`{"image":%q}`, remote.URL+"/never-finishes.png")
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	startedAt := time.Now()
	_, _, err := server.parseEditRequest(request)
	duration := time.Since(startedAt)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want download deadline exceeded", err)
	}
	if duration > time.Second {
		t.Fatalf("download took %s, want bounded read timeout", duration)
	}
	select {
	case <-started:
	default:
		t.Fatal("remote body was never opened")
	}
}
