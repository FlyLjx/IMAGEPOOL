package postprocess

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepool/internal/config"
)

func comparisonTestPNG(t *testing.T, value color.RGBA) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			canvas.SetRGBA(x, y, value)
		}
	}
	buffer := new(bytes.Buffer)
	if err := png.Encode(buffer, canvas); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type fakeRunner struct {
	result Result
	calls  int
}

func (r *fakeRunner) Process(context.Context, workerConfig, []byte, Options) Result {
	r.calls++
	return r.result
}
func (r *fakeRunner) Running() bool { return r.calls > 0 }
func (r *fakeRunner) Close()        {}

func TestDisabledPostprocessReturnsOriginal(t *testing.T) {
	runner := &fakeRunner{}
	service := newService(config.Default(), runner)
	defer service.Close()
	result := service.Process(context.Background(), []byte("original"), Options{HDRepair: true, RequestedSize: "1024x1024"})
	if string(result.Data) != "original" || !result.Skipped || runner.calls != 0 {
		t.Fatalf("result=%#v calls=%d", result, runner.calls)
	}
}

func TestEnabledPostprocessUsesDedicatedWorker(t *testing.T) {
	cfg := config.Default()
	cfg.ImageSuperResolutionEnabled = true
	runner := &fakeRunner{result: Result{Data: []byte("processed"), SuperResolved: true}}
	service := newService(cfg, runner)
	defer service.Close()
	result := service.Process(context.Background(), []byte("original"), Options{
		ParentTaskID:    "img_parent",
		OwnerID:         "admin",
		Model:           "gpt-image-2-2k",
		RequestedSize:   "2048x2048",
		SuperResolution: true,
	})
	if string(result.Data) != "processed" || !result.SuperResolved || runner.calls != 1 {
		t.Fatalf("result=%#v calls=%d", result, runner.calls)
	}
	stats := service.Stats()
	if stats.Processed != 1 || stats.SuperResolved != 1 || stats.WorkerLimit != 1 {
		t.Fatalf("stats=%#v", stats)
	}
	history, err := service.History(1, 50, "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	if history.Total != 1 || len(history.Items) != 1 {
		t.Fatalf("history=%#v", history)
	}
	task := history.Items[0]
	if task.ParentTaskID != "img_parent" || task.Model != "gpt-image-2-2k" || task.Status != "success" || !task.SuperResolved || !task.ForceSuperResolution || task.OutputBytes != len("processed") {
		t.Fatalf("postprocess task=%#v", task)
	}
}

func TestRequestCanForceSuperResolutionWhenGlobalSwitchIsDisabled(t *testing.T) {
	runner := &fakeRunner{result: Result{Data: []byte("processed"), SuperResolved: true}}
	service := newService(config.Default(), runner)
	defer service.Close()
	result := service.Process(context.Background(), []byte("original"), Options{
		RequestedSize:   "2048x2048",
		SuperResolution: true,
	})
	if string(result.Data) != "processed" || !result.SuperResolved || runner.calls != 1 {
		t.Fatalf("result=%#v calls=%d", result, runner.calls)
	}
}

func TestSuccessfulTaskPersistsComparisonImages(t *testing.T) {
	cfg := config.Default()
	cfg.ImageOutputDir = t.TempDir()
	cfg.ImageSuperResolutionEnabled = true
	before := comparisonTestPNG(t, color.RGBA{R: 0xff, A: 0xff})
	after := comparisonTestPNG(t, color.RGBA{G: 0xff, A: 0xff})
	runner := &fakeRunner{result: Result{Data: after, SuperResolved: true}}
	service := newService(cfg, runner)
	defer service.Close()

	result := service.Process(context.Background(), before, Options{ParentTaskID: "img_parent"})
	if !result.SuperResolved {
		t.Fatalf("result=%#v", result)
	}
	history, err := service.History(1, 10, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 1 {
		t.Fatalf("history=%#v", history)
	}
	task := history.Items[0]
	if !strings.HasPrefix(task.InputImagePath, ".postprocess-comparisons/") || !strings.HasPrefix(task.OutputImagePath, ".postprocess-comparisons/") {
		t.Fatalf("comparison paths=%q %q", task.InputImagePath, task.OutputImagePath)
	}
	inputData, err := os.ReadFile(filepath.Join(cfg.ImageOutputDir, filepath.FromSlash(task.InputImagePath)))
	if err != nil {
		t.Fatal(err)
	}
	outputData, err := os.ReadFile(filepath.Join(cfg.ImageOutputDir, filepath.FromSlash(task.OutputImagePath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(inputData, before) || !bytes.Equal(outputData, after) {
		t.Fatal("persisted comparison images do not match processing input/output")
	}
}
